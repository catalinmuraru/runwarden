package api

import (
	"github.com/catalinmuraru/runwarden/internal/authz"
	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/history"
)

// resolveKube returns the kubeconfig pinning a run of sc to the cluster of its target
// environment (and of its source, for two-environment runs), with what may be shown about those
// clusters. Scripts without "kubernetes": true get none.
func (s *Server) resolveKube(sc *config.Script, uses []authz.EnvUse) (*executor.Kube, []history.Cluster, error) {
	if !sc.Kubernetes {
		return nil, nil, nil
	}
	targets, sources := splitUses(uses)
	res, err := s.opts.Kube.Resolve(targets, sources)
	if err != nil {
		return nil, nil, err
	}
	clusters := make([]history.Cluster, len(res.Clusters))
	for i, c := range res.Clusters {
		clusters[i] = history.Cluster{Environment: c.Environment, Server: c.Server, Namespace: c.Namespace}
	}
	return &executor.Kube{Config: res.Kubeconfig, Env: res.Env}, clusters, nil
}
