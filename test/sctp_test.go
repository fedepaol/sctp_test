package test_test

import (
	"fmt"
	"log"
	"os"
	"time"

	igntypes "github.com/coreos/ignition/config/v2_2/types"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"

	v1 "github.com/openshift/api/config/v1"
	configClientv1 "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	k8sv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	mcfgClient "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type testClients struct {
	k8sClient       *kubernetes.Clientset
	ocpConfigClient *configClientv1.ConfigV1Client
	mcoClient       *mcfgClient.Clientset
}

var clients *testClients

var _ = BeforeSuite(func() {
	clients = setupClients()
})

var _ = Describe("TestSctp", func() {

	beforeAll(func() {
		openFeaturegate(clients.ocpConfigClient)
		applySELinuxPolicy(clients.k8sClient)
		applyMC(clients.mcoClient, clients.k8sClient)
		createSctpService(clients.k8sClient)
	})

	var _ = Describe("Client Server Connection", func() {
		var clientNode string
		var serverNode string
		It("Should fetch client and server node", func() {
			nodes, err := clients.k8sClient.CoreV1().Nodes().List(metav1.ListOptions{
				LabelSelector: "node-role.kubernetes.io/worker=",
			})

			Expect(err).ToNot(HaveOccurred())
			clientNode = nodes.Items[0].Name
			serverNode = nodes.Items[0].Name
			if len(nodes.Items) > 1 {
				serverNode = nodes.Items[1].Name
			}
		})
		var err error
		var serverPod *k8sv1.Pod
		It("Should create the server pod", func() {
			serverArgs := []string{"sctp_test -H localhost -P 30101 -l 2>&1 > sctp.log & while sleep 10; do if grep --quiet SHUTDOWN sctp.log; then exit 0; fi; done"}
			pod := JobForNode("scptserver", serverNode, "sctpserver", []string{"/bin/bash", "-c"}, serverArgs)
			pod.Spec.Containers[0].Ports = []k8sv1.ContainerPort{
				k8sv1.ContainerPort{
					Name:          "sctpport",
					Protocol:      k8sv1.ProtocolSCTP,
					ContainerPort: 30101,
				},
			}
			serverPod, err = clients.k8sClient.CoreV1().Pods("default").Create(pod)
			Expect(err).ToNot(HaveOccurred())
		})

		var runningPod *k8sv1.Pod

		It("Should fetch the address of the server pod", func() {
			Eventually(func() k8sv1.PodPhase {
				runningPod, err = clients.k8sClient.CoreV1().Pods("default").Get(serverPod.Name, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				return runningPod.Status.Phase
			}, 3*time.Minute, 1*time.Second).Should(Equal(k8sv1.PodRunning))
		})

		It("Should create the client pod", func() {
			clientArgs := []string{fmt.Sprintf("sctp_test -H localhost -P 30100 -h %s -p 30101 -s", runningPod.Status.PodIP)}
			clientPod := JobForNode("sctpclient", clientNode, "sctpclient", []string{"/bin/bash", "-c"}, clientArgs)
			clients.k8sClient.CoreV1().Pods("default").Create(clientPod)
		})

		It("Should complete the server pod", func() {
			Eventually(func() k8sv1.PodPhase {
				pod, err := clients.k8sClient.CoreV1().Pods("default").Get(serverPod.Name, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				return pod.Status.Phase
			}, 1*time.Minute, 1*time.Second).Should(Equal(k8sv1.PodSucceeded))
		})
	})
})

func setupClients() *testClients {
	kubeconfig := os.Getenv("KUBECONFIG")
	if len(kubeconfig) < 0 {
		log.Fatalf("No kubeconfig defined")
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		log.Fatal("Failed to build config", err)
	}

	res := testClients{}
	res.k8sClient = kubernetes.NewForConfigOrDie(config)
	res.ocpConfigClient = configClientv1.NewForConfigOrDie(config)
	res.mcoClient = mcfgClient.NewForConfigOrDie(config)
	return &res
}

func openFeaturegate(client *configClientv1.ConfigV1Client) {
	Context("Open Feature Gate", func() {
		fg, err := client.FeatureGates().Get("cluster", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())

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

		_, err = client.FeatureGates().Update(fg)
		Expect(err).ToNot(HaveOccurred())
	})
}

func applyMC(client *mcfgClient.Clientset, k8sClient *kubernetes.Clientset) {
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

	waitForSctpReady(k8sClient)
}

func waitForSctpReady(client *kubernetes.Clientset) {
	nodes, err := client.CoreV1().Nodes().List(metav1.ListOptions{
		LabelSelector: "node-role.kubernetes.io/worker=",
	})
	Expect(err).ToNot(HaveOccurred())

	args := []string{`set -x; x="$(checksctp 2>&1)"; echo "$x" ; if [ "$x" = "SCTP supported" ]; then echo "succeeded"; exit 0; else echo "failed"; exit 1; fi`}
	for _, n := range nodes.Items {
		job := JobForNode("checksctp", n.Name, "checksctp", []string{"/bin/bash", "-c"}, args)
		client.CoreV1().Pods("default").Create(job)
	}

	Eventually(func() bool {
		pods, err := client.CoreV1().Pods("default").List(metav1.ListOptions{LabelSelector: "app=checksctp"})
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		for _, p := range pods.Items {
			if p.Status.Phase != k8sv1.PodSucceeded {
				return false
			}
		}
		return true
	}, 10*time.Minute, 10*time.Second).Should(Equal(true)) // long timeout since this requires reboots
}

func applySELinuxPolicy(client *kubernetes.Clientset) {
	nodes, err := client.CoreV1().Nodes().List(metav1.ListOptions{
		LabelSelector: "node-role.kubernetes.io/worker=",
	})
	Expect(err).ToNot(HaveOccurred())
	for _, n := range nodes.Items {
		createSEPolicyPods(client, n.Name)
	}

	Eventually(func() bool {
		pods, err := client.CoreV1().Pods("default").List(metav1.ListOptions{LabelSelector: "app=sctppolicy"})
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		for _, p := range pods.Items {
			if p.Status.Phase != k8sv1.PodSucceeded {
				return false
			}
		}
		return true
	}, 3*time.Minute, 1*time.Second).Should(Equal(true))
}

func createSEPolicyPods(client *kubernetes.Clientset, node string) {
	pod := k8sv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "sctppolicy",
			Labels: map[string]string{
				"app": "sctppolicy",
			},
		},
		Spec: k8sv1.PodSpec{
			RestartPolicy: k8sv1.RestartPolicyNever,
			Containers: []k8sv1.Container{
				{
					Name:    "sepolicy",
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

func createSctpService(client *kubernetes.Clientset) {
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
	Eventually(func() error {
		_, err := client.CoreV1().Services("default").Create(&service)
		return err
	}, 3*time.Minute, 1*time.Second).ShouldNot(HaveOccurred())
}

func JobForNode(name, node, app string, cmd []string, args []string) *k8sv1.Pod {
	job := k8sv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: name,
			Labels: map[string]string{
				"app": app,
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

func beforeAll(fn func()) {
	first := true
	BeforeEach(func() {
		if first {
			fn()
			first = false
		}
	})
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
