package oinc

import (
	"github.com/jasonmadigan/oinc/pkg/runtime"
)

type Status struct {
	Container string `json:"container"`
	APIServer string `json:"apiserver"`
	Error     string `json:"error,omitempty"`
}

func GetStatus(runtimeOverride string) Status {
	rt, err := runtime.Detect(runtimeOverride)
	if err != nil {
		return Status{Error: err.Error()}
	}

	s := Status{Container: "stopped", APIServer: "unreachable"}

	if rt.ContainerRunning(containerName) {
		s.Container = "running"
		s.APIServer = "https://127.0.0.1:6443"
	} else if rt.ContainerExists(containerName) {
		s.Container = "stopped"
	} else {
		s.Container = "not found"
	}

	return s
}
