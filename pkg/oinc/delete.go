package oinc

import (
	"log/slog"

	"github.com/jasonmadigan/oinc/pkg/kubeconfig"
	"github.com/jasonmadigan/oinc/pkg/runtime"
)

func Delete(runtimeOverride string, logger *slog.Logger) error {
	rt, err := runtime.Detect(runtimeOverride)
	if err != nil {
		return err
	}

	logger.Info("removing container", "name", containerName)
	if err := rt.RemoveContainer(containerName); err != nil {
		logger.Warn("failed to remove container", "err", err)
	}

	logger.Info("removing console container", "name", consoleContainer)
	if err := rt.RemoveContainer(consoleContainer); err != nil {
		logger.Warn("failed to remove console container", "err", err)
	}

	if err := kubeconfig.Remove(); err != nil {
		logger.Warn("failed to clean kubeconfig", "err", err)
	}

	logger.Info("cluster deleted")
	return nil
}
