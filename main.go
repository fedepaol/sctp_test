package main

import (
	"fmt"
	"log"
	"os"
	"time"

	v1 "github.com/openshift/api/config/v1"
	configClientv1 "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"

	igntypes "github.com/coreos/ignition/config/v2_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfgClient "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	k8sv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	kubeconfig := os.Getenv("KUBECONFIG")
	if len(kubeconfig) < 0 {
		log.Fatalf("No kubeconfig defined")
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		log.Fatal("Failed to build config", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal("Failed to get client", err)
	}

	nodes, err := clientset.CoreV1().Nodes().List(metav1.ListOptions{
		LabelSelector: "node-role.kubernetes.io/worker=",
	})
	if err != nil {
		log.Fatal("Failed to get nodes", err)
	}
	for _, n := range nodes.Items {
		fmt.Println("Creating sepolicy pod on node ", n.Name)
		createPolicyJob(n.Name, n.Name, clientset)
	}
	mcoClient := mcfgClient.NewForConfigOrDie(config)

	applyMC(mcoClient)

	configClient := configClientv1.NewForConfigOrDie(config)
	openFeaturegate(configClient)

	fmt.Println("Checking featureset")
	createServiceAndCheckFeatureGate(clientset)

	fmt.Println("Checking ready")
	checkSctpReady(clientset, nodes.Items)
	testClientServerConn(clientset, nodes.Items)

}

func testClientServerConn(client *kubernetes.Clientset, nodes []k8sv1.Node) {
	firstNode := nodes[0].Name

	serverNode := firstNode
	if len(nodes) > 1 {
		serverNode = nodes[1].Name
	}
	serverArgs := []string{"testsctp -H localhost -P 30101 -L | grep SHUTDOWN && exit 0"}

	serverPod := RenderJob("scptserver", serverNode, []string{"/bin/bash", "-c"}, serverArgs)
	serverPod.Spec.Containers[0].Ports = []k8sv1.ContainerPort{
		k8sv1.ContainerPort{
			Name:          "sctpport",
			Protocol:      k8sv1.ProtocolSCTP,
			ContainerPort: 30100,
		},
	}

	server, err := client.CoreV1().Pods("default").Create(serverPod)
	if err != nil {
		log.Fatalf("Failed to create server %v", err)
	}
	podAddress := server.Status.PodIP
	clientArgs := []string{fmt.Sprintf("testsctp -H localhost -P 30101 -h %s -p 30100 -s", podAddress)}
	clientPod := RenderJob("sctpclient", firstNode, []string{"/bin/bash", "-c"}, clientArgs)
	client.CoreV1().Pods("default").Create(clientPod)

	for {
		pod, err := client.CoreV1().Pods("default").Get("sctpserver", metav1.GetOptions{})
		if err != nil {
			log.Fatalf("Failed to fetch sctpserver pod")
		}

		if pod.Status.Phase != k8sv1.PodSucceeded {
			log.Println("sctp server in phase ", pod.Status.Phase)
			continue
		}

		break
	}

	fmt.Println("Succeed!")

}

func openFeaturegate(client *configClientv1.ConfigV1Client) {
	fg, err := client.FeatureGates().Get("cluster", metav1.GetOptions{})
	if err != nil {
		log.Fatalf("Failed to get cluster featuregate")
	}
	fg.Spec = v1.FeatureGateSpec{
		FeatureGateSelection: v1.FeatureGateSelection{
			FeatureSet: "CustomNoUpgrade",
			CustomNoUpgrade: &v1.CustomFeatureGates{
				Enabled: []string{
					"SCTPSupport",
				},
			},
		},
	}

	res, err := client.FeatureGates().Update(fg)
	if err != nil {
		log.Fatalf("Creation failed %v", err)
	}
	log.Println("Created ", *res)
}

func createPolicyJob(name string, node string, client *kubernetes.Clientset) {

	pod := k8sv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"app": "sctppolicy",
			},
		},
		Spec: k8sv1.PodSpec{
			RestartPolicy: k8sv1.RestartPolicyNever,
			Containers: []k8sv1.Container{
				{
					Name:    name,
					Image:   "fedepaol/sctpsepolicy:v1",
					Command: []string{"/bin/sh", "-c"},
					Args: []string{`cp newsctp.pp /host/tmp;
							echo "applying policy";
					        chroot /host /usr/sbin/semodule -i /tmp/newsctp.pp;
					        echo "policy applied";`},
					SecurityContext: &k8sv1.SecurityContext{
						Privileged: newBool(true),
					},
					VolumeMounts: []k8sv1.VolumeMount{
						k8sv1.VolumeMount{
							Name:      "host",
							MountPath: "/host",
						},
					},
				},
			},
			Volumes: []k8sv1.Volume{
				k8sv1.Volume{
					Name: "host",
					VolumeSource: k8sv1.VolumeSource{
						HostPath: &k8sv1.HostPathVolumeSource{
							Path: "/",
							Type: newHostPath(k8sv1.HostPathDirectory),
						},
					},
				},
			},
			NodeSelector: map[string]string{
				"kubernetes.io/hostname": node,
			},
		},
	}

	_, err := client.CoreV1().Pods("default").Create(&pod)
	if err != nil {
		log.Fatal("Failed to create policy pod", err)
	}
}

func applyMC(client *mcfgClient.Clientset) error {
	mc := mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "allow-sctp",
			Labels: map[string]string{
				"machineconfiguration.openshift.io/role": "worker",
			},
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: igntypes.Config{
				Ignition: igntypes.Ignition{
					Version: "2.2.0",
				},
				Storage: igntypes.Storage{
					Files: []igntypes.File{
						igntypes.File{
							Node: igntypes.Node{
								Filesystem: "root",
								Path:       "/etc/modprobe.d/sctp-blacklist.conf",
							},
							FileEmbedded1: igntypes.FileEmbedded1{
								Mode: newInt(420),
								Contents: igntypes.FileContents{
									Source: "data:,",
								},
							},
						},
						igntypes.File{
							Node: igntypes.Node{
								Filesystem: "root",
								Path:       "/etc/modules-load.d/sctp-load.conf",
							},
							FileEmbedded1: igntypes.FileEmbedded1{
								Mode: newInt(420),
								Contents: igntypes.FileContents{
									Source: "data:text/plain;charset=utf-8;base64,c2N0cA==",
								},
							},
						},
					},
				},
			},
		},
	}
	client.MachineconfigurationV1().MachineConfigs().Create(&mc)
	return nil
}

func createSctpService(client *kubernetes.Clientset) error {
	service := k8sv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sctpservice",
		},
		Spec: k8sv1.ServiceSpec{
			Selector: map[string]string{
				"app": "sctpserver",
			},
			Ports: []k8sv1.ServicePort{
				k8sv1.ServicePort{
					Protocol: k8sv1.ProtocolSCTP,
					Port:     30101,
					TargetPort: intstr.IntOrString{
						Type:   intstr.String,
						StrVal: "sctpserver",
					},
				},
			},
		},
	}
	_, err := client.CoreV1().Services("default").Create(&service)
	if err != nil {
		return fmt.Errorf("Failed to create service %v", err)
	}
	return nil
}

func checkSctpReady(client *kubernetes.Clientset, nodes []k8sv1.Node) error {
	args := []string{`set -x; x="$(checksctp 2>&1)"; echo "$x" ; if [ "$x" = "SCTP supported" ]; then echo "succeeded"; exit 0; else echo "failed"; exit 1; fi`}
	for _, n := range nodes {
		fmt.Println("Creating job on node ", n.Name)
		job := RenderJob("checksctp"+n.Name, n.Name, []string{"/bin/bash", "-c"}, args)
		client.CoreV1().Pods("default").Create(job)
	}

Exit:
	for {
		pods, err := client.CoreV1().Pods("default").List(metav1.ListOptions{LabelSelector: "app=test"})
		if err != nil {
			return fmt.Errorf("Failed to retrieve test pods, %v", err)
		}
		time.Sleep(2 * time.Second)
		for _, p := range pods.Items {
			if p.Status.Phase != k8sv1.PodSucceeded {
				fmt.Printf("Pod %s in phase %v\n", p.Name, p.Status.Phase)
				continue Exit
			} else {
				fmt.Printf("Pod111 %s in phase %v\n", p.Name, p.Status.Phase)
			}
		}
		break
	}
	return nil
}

func RenderJob(name string, node string, cmd []string, args []string) *k8sv1.Pod {
	job := k8sv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: name,
			Labels: map[string]string{
				"app": "test",
			},
		},
		Spec: k8sv1.PodSpec{
			RestartPolicy: k8sv1.RestartPolicyNever,
			Containers: []k8sv1.Container{
				{
					Name:    name,
					Image:   "quay.io/wcaban/net-toolbox:latest",
					Command: cmd,
					Args:    args,
				},
			},
			NodeSelector: map[string]string{
				"kubernetes.io/hostname": node,
			},
		},
	}

	return &job
}

func createServiceAndCheckFeatureGate(client *kubernetes.Clientset) {
	for {
		err := createSctpService(client)
		if err != nil {
			log.Println("Failed to create sctp service for ", err)
			continue
		}
		break
	}
}

func newBool(x bool) *bool {
	return &x
}

func newInt(x int) *int {
	return &x
}

func newHostPath(x k8sv1.HostPathType) *k8sv1.HostPathType {
	return &x
}
