package cluster

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var coreNamespaces = []string{
	"kube-proxy",
	"kube-system",
	"openshift-dns",
	"openshift-ingress",
	"openshift-service-ca",
}

func WaitForReady(kubeconfig []byte, retries int, delay time.Duration) error {
	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("building rest config: %w", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	// wait for node to be ready first
	for i := range retries {
		nodes, err := client.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		if err == nil && len(nodes.Items) > 0 {
			for _, cond := range nodes.Items[0].Status.Conditions {
				if cond.Type == "Ready" && cond.Status == "True" {
					goto nodeReady
				}
			}
		}
		if i < retries-1 {
			time.Sleep(delay)
		}
	}
	return fmt.Errorf("node not ready after %d attempts", retries)
nodeReady:

	for i := range retries {
		allReady := true
		for _, ns := range coreNamespaces {
			if err := checkNamespaceReady(client, ns); err != nil {
				allReady = false
				break
			}
		}
		if allReady {
			return nil
		}
		if i < retries-1 {
			time.Sleep(delay)
		}
	}
	return fmt.Errorf("pods not ready after %d attempts", retries)
}

func checkNamespaceReady(client kubernetes.Interface, ns string) error {
	pods, err := client.CoreV1().Pods(ns).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing pods in %s: %w", ns, err)
	}

	if len(pods.Items) == 0 {
		return fmt.Errorf("no pods in %s", ns)
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase != "Running" {
			return fmt.Errorf("pod %s in %s is %s", pod.Name, ns, pod.Status.Phase)
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if !cs.Ready {
				return fmt.Errorf("container %s/%s not ready", pod.Name, cs.Name)
			}
		}
	}
	return nil
}
