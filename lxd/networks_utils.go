package main

import (
	"github.com/lxc/lxd/lxd/cluster"
	"github.com/lxc/lxd/lxd/db"
	"github.com/lxc/lxd/lxd/network"
	"github.com/lxc/lxd/lxd/project"
	"github.com/lxc/lxd/lxd/state"
	"github.com/lxc/lxd/shared/logger"
)

func networkAutoAttach(cluster *db.Cluster, devName string) error {
	_, dbInfo, err := cluster.GetNetworkWithInterface(devName)
	if err != nil {
		// No match found, move on
		return nil
	}

	return network.AttachInterface(dbInfo.Name, devName)
}

// networkUpdateForkdnsServersTask runs every 30s and refreshes the forkdns servers list.
func networkUpdateForkdnsServersTask(s *state.State, heartbeatData *cluster.APIHeartbeat) error {
	// Use project.Default here as forkdns (fan bridge) networks don't support projects.
	projectName := project.Default

	// Get a list of managed networks
	networks, err := s.Cluster.GetCreatedNetworks(projectName)
	if err != nil {
		return err
	}

	for _, name := range networks {
		n, err := network.LoadByName(s, projectName, name)
		if err != nil {
			logger.Errorf("Failed to load network %q from project %q for heartbeat", name, projectName)
			continue
		}

		if n.Type() == "bridge" && n.Config()["bridge.mode"] == "fan" {
			err := n.HandleHeartbeat(heartbeatData)
			if err != nil {
				return err
			}
		}
	}

	return nil
}
