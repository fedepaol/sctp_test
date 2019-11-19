package main

import (
	"fmt"
	"log"
	"os"

	v1 "github.com/openshift/api/config/v1"
	configClientv1 "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"

	igntypes "github.com/coreos/ignition/config/v2_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfgClient "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	k8sv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	nodes, err := clientset.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		log.Fatal("Failed to get nodes", err)
	}
	for _, n := range nodes.Items {
		fmt.Println("Creating sepolicy pod on node ", n.Name)
		createPolicyJob(n.Name, n.Name, clientset)
	}
	mcoClient := mcfgClient.NewForConfigOrDie(config)

	applyMC(mcoClient)
	clientset.CoreV1().Pods("default").List(metav1.ListOptions{})

	configClient := configClientv1.NewForConfigOrDie(config)
	openFeaturegate(configClient)

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
						RunAsUser:  new(int64),
					},
				},
			},
			SecurityContext: &k8sv1.PodSecurityContext{
				RunAsUser: new(int64),
			},
			NodeSelector: map[string]string{
				"kubernetes.io/hostname": node,
			},
		},
	}

	_, err := client.CoreV1().Pods("default").Create(&pod)
	if err != nil {
		log.Fatalf("Failed to create policy pod")
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

func newBool(x bool) *bool {
	return &x
}

func newInt(x int) *int {
	return &x
}
