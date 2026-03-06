package kubeconfig

import (
	"fmt"
	"os"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

const clusterName = "oinc"

func path() string {
	if p := os.Getenv("KUBECONFIG"); p != "" {
		return p
	}
	return clientcmd.RecommendedHomeFile
}

// Read returns the raw kubeconfig bytes from the user's kubeconfig file.
func Read() ([]byte, error) {
	return os.ReadFile(path())
}

// Update merges the given kubeconfig bytes into the user's kubeconfig file.
func Update(raw []byte) error {
	existing, err := clientcmd.LoadFromFile(path())
	if err != nil {
		existing = api.NewConfig()
	}

	incoming, err := clientcmd.Load(raw)
	if err != nil {
		return fmt.Errorf("parsing kubeconfig: %w", err)
	}

	for name, cluster := range incoming.Clusters {
		existing.Clusters[name] = cluster
	}
	for name, ctx := range incoming.Contexts {
		existing.Contexts[name] = ctx
	}
	for name, auth := range incoming.AuthInfos {
		existing.AuthInfos[name] = auth
	}
	existing.CurrentContext = incoming.CurrentContext

	return clientcmd.WriteToFile(*existing, path())
}

// Remove deletes the oinc cluster/context/user from the kubeconfig.
func Remove() error {
	config, err := clientcmd.LoadFromFile(path())
	if err != nil {
		return nil
	}

	if _, exists := config.Clusters[clusterName]; !exists {
		return nil
	}

	delete(config.Clusters, clusterName)

	for ctxName, ctx := range config.Contexts {
		if ctx.Cluster == clusterName {
			if config.CurrentContext == ctxName {
				config.CurrentContext = ""
			}
			delete(config.AuthInfos, ctx.AuthInfo)
			delete(config.Contexts, ctxName)
		}
	}

	return clientcmd.WriteToFile(*config, path())
}
