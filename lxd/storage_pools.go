package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/mux"
	"github.com/pkg/errors"

	lxd "github.com/lxc/lxd/client"
	"github.com/lxc/lxd/lxd/cluster"
	"github.com/lxc/lxd/lxd/cluster/request"
	"github.com/lxc/lxd/lxd/db"
	"github.com/lxc/lxd/lxd/project"
	"github.com/lxc/lxd/lxd/response"
	storagePools "github.com/lxc/lxd/lxd/storage"
	"github.com/lxc/lxd/lxd/util"
	"github.com/lxc/lxd/shared"
	"github.com/lxc/lxd/shared/api"
	log "github.com/lxc/lxd/shared/log15"
	"github.com/lxc/lxd/shared/logger"
	"github.com/lxc/lxd/shared/version"
)

// Lock to prevent concurent storage pools creation
var storagePoolCreateLock sync.Mutex

var storagePoolsCmd = APIEndpoint{
	Path: "storage-pools",

	Get:  APIEndpointAction{Handler: storagePoolsGet, AccessHandler: allowAuthenticated},
	Post: APIEndpointAction{Handler: storagePoolsPost},
}

var storagePoolCmd = APIEndpoint{
	Path: "storage-pools/{name}",

	Delete: APIEndpointAction{Handler: storagePoolDelete},
	Get:    APIEndpointAction{Handler: storagePoolGet, AccessHandler: allowAuthenticated},
	Patch:  APIEndpointAction{Handler: storagePoolPatch},
	Put:    APIEndpointAction{Handler: storagePoolPut},
}

// /1.0/storage-pools
// List all storage pools.
func storagePoolsGet(d *Daemon, r *http.Request) response.Response {
	recursion := util.IsRecursionRequest(r)

	pools, err := d.cluster.GetStoragePoolNames()
	if err != nil && err != db.ErrNoSuchObject {
		return response.SmartError(err)
	}

	resultString := []string{}
	resultMap := []api.StoragePool{}
	for _, pool := range pools {
		if !recursion {
			resultString = append(resultString, fmt.Sprintf("/%s/storage-pools/%s", version.APIVersion, pool))
		} else {
			_, pl, _, err := d.cluster.GetStoragePoolInAnyState(pool)
			if err != nil {
				continue
			}

			// Get all users of the storage pool.
			poolUsedBy := []string{}
			err = d.cluster.Transaction(func(tx *db.ClusterTx) error {
				poolUsedBy, err = tx.GetStoragePoolUsedBy(pool)
				return err
			})
			if err != nil {
				return response.SmartError(err)
			}
			pl.UsedBy = project.FilterUsedBy(r, poolUsedBy)

			resultMap = append(resultMap, *pl)
		}
	}

	if !recursion {
		return response.SyncResponse(true, resultString)
	}

	return response.SyncResponse(true, resultMap)
}

// /1.0/storage-pools
// Create a storage pool.
func storagePoolsPost(d *Daemon, r *http.Request) response.Response {
	storagePoolCreateLock.Lock()
	defer storagePoolCreateLock.Unlock()

	req := api.StoragePoolsPost{}

	// Parse the request.
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		return response.BadRequest(err)
	}

	// Sanity checks.
	if req.Name == "" {
		return response.BadRequest(fmt.Errorf("No name provided"))
	}

	if strings.Contains(req.Name, "/") {
		return response.BadRequest(fmt.Errorf("Storage pool names may not contain slashes"))
	}

	if req.Driver == "" {
		return response.BadRequest(fmt.Errorf("No driver provided"))
	}

	url := fmt.Sprintf("/%s/storage-pools/%s", version.APIVersion, req.Name)
	resp := response.SyncResponseLocation(true, nil, url)

	clientType := request.UserAgentClientType(r.Header.Get("User-Agent"))

	if isClusterNotification(r) {
		// This is an internal request which triggers the actual
		// creation of the pool across all nodes, after they have been
		// previously defined.
		err = storagePoolValidate(req.Name, req.Driver, req.Config)
		if err != nil {
			return response.BadRequest(err)
		}

		poolID, err := d.cluster.GetStoragePoolID(req.Name)
		if err != nil {
			return response.NotFound(err)
		}

		_, err = storagePoolCreateLocal(d.State(), poolID, req, clientType)
		if err != nil {
			return response.SmartError(err)
		}

		return resp
	}

	targetNode := queryParam(r, "target")
	if targetNode != "" {
		// A targetNode was specified, let's just define the node's storage without actually creating it.
		// The only legal key values for the storage config are the ones in StoragePoolNodeConfigKeys.
		for key := range req.Config {
			if !shared.StringInSlice(key, db.StoragePoolNodeConfigKeys) {
				return response.SmartError(fmt.Errorf("Config key %q may not be used as node-specific key", key))
			}
		}

		err = storagePoolValidate(req.Name, req.Driver, req.Config)
		if err != nil {
			return response.BadRequest(err)
		}

		err = d.cluster.Transaction(func(tx *db.ClusterTx) error {
			return tx.CreatePendingStoragePool(targetNode, req.Name, req.Driver, req.Config)
		})
		if err != nil {
			if err == db.ErrAlreadyDefined {
				return response.BadRequest(fmt.Errorf("The storage pool already defined on node %q", targetNode))
			}

			return response.SmartError(err)
		}

		return resp
	}

	// Load existing pool if exists, if not don't fail.
	_, pool, _, err := d.cluster.GetStoragePoolInAnyState(req.Name)
	if err != nil && err != db.ErrNoSuchObject {
		return response.InternalError(err)
	}

	// Check if we're clustered.
	count, err := cluster.Count(d.State())
	if err != nil {
		return response.SmartError(err)
	}

	// No targetNode was specified and we're clustered or there is an existing partially created single node
	// pool, either way finalize the config in the db and actually create the pool on all node in the cluster.
	if count > 1 || (pool != nil && pool.Status != api.StoragePoolStatusCreated) {
		err = storagePoolsPostCluster(d, pool, req, clientType)
		if err != nil {
			return response.InternalError(err)
		}

		return resp
	}

	// Create new single node storage pool.
	err = storagePoolCreateGlobal(d.State(), req, clientType)
	if err != nil {
		return response.InternalError(err)
	}

	return resp
}

// storagePoolPartiallyCreated returns true of supplied storage pool has properties that indicate it has had
// previous create attempts run on it but failed on one or more nodes.
func storagePoolPartiallyCreated(pool *api.StoragePool) bool {
	// If the pool status is StoragePoolStatusErrored, this means create has been run in the past and has
	// failed on one or more nodes. Hence it is partially created.
	if pool.Status == api.StoragePoolStatusErrored {
		return true
	}

	// If the pool has global config keys, then it has previously been created by having its global config
	// inserted, and this means it is partialled created.
	for key := range pool.Config {
		if !shared.StringInSlice(key, db.StoragePoolNodeConfigKeys) {
			return true
		}
	}

	return false
}

// storagePoolsPostCluster handles creating storage pools after the per-node config records have been created.
// Accepts an optional existing pool record, which will exist when performing subsequent re-create attempts.
func storagePoolsPostCluster(d *Daemon, pool *api.StoragePool, req api.StoragePoolsPost, clientType request.ClientType) error {
	// Check that no node-specific config key has been defined.
	for key := range req.Config {
		if shared.StringInSlice(key, db.StoragePoolNodeConfigKeys) {
			return fmt.Errorf("Config key %q is node-specific", key)
		}
	}

	// Perform sanity checks if pool already exists.
	if pool != nil {
		// Check pool isn't already created.
		if pool.Status == api.StoragePoolStatusCreated {
			return fmt.Errorf("The storage pool is already created")
		}

		// Check the requested pool type matches the type created when adding the local node config.
		if req.Driver != pool.Driver {
			return fmt.Errorf("Requested storage pool driver %q doesn't match driver in existing database record %q", req.Driver, pool.Driver)
		}
	}

	// Check that the pool is properly defined, fetch the node-specific configs and insert the global config.
	var configs map[string]map[string]string
	var nodeName string
	var poolID int64
	err := d.cluster.Transaction(func(tx *db.ClusterTx) error {
		var err error

		// Check that the pool was defined at all. Must come before partially created checks.
		poolID, err = tx.GetStoragePoolID(req.Name)
		if err != nil {
			return err
		}

		// Check if any global config exists already, if so we should not create global config again.
		if pool != nil && storagePoolPartiallyCreated(pool) {
			if len(req.Config) > 0 {
				return fmt.Errorf("Storage pool already partially created. Please do not specify any global config when re-running create")
			}

			logger.Debug("Skipping global storage pool create as global config already partially created", log.Ctx{"pool": req.Name})
			return nil
		}

		// Fetch the node-specific configs and check the pool is defined for all nodes.
		configs, err = tx.GetStoragePoolNodeConfigs(poolID)
		if err != nil {
			return err
		}

		// Take note of the name of this node
		nodeName, err = tx.GetLocalNodeName()
		if err != nil {
			return err
		}

		// Insert the global config keys.
		err = tx.CreateStoragePoolConfig(poolID, 0, req.Config)
		if err != nil {
			return err
		}

		// Assume failure unless we succeed later on.
		return tx.StoragePoolErrored(req.Name)
	})
	if err != nil {
		if err == db.ErrNoSuchObject {
			return fmt.Errorf("Pool not pending on any node (use --target <node> first)")
		}
		return err
	}

	// Create notifier for other nodes to create the storage pool.
	notifier, err := cluster.NewNotifier(d.State(), d.endpoints.NetworkCert(), cluster.NotifyAll)
	if err != nil {
		return err
	}

	// Create the pool on this node.
	nodeReq := req

	// Merge node specific config items into global config.
	for key, value := range configs[nodeName] {
		nodeReq.Config[key] = value
	}

	err = storagePoolValidate(req.Name, req.Driver, nodeReq.Config)
	if err != nil {
		return err
	}

	updatedConfig, err := storagePoolCreateLocal(d.State(), poolID, req, clientType)
	if err != nil {
		return err
	}
	req.Config = updatedConfig
	logger.Debug("Created storage pool on local cluster member", log.Ctx{"pool": req.Name})

	// Strip node specific config keys from config. Very important so we don't forward node-specific config.
	for _, k := range db.StoragePoolNodeConfigKeys {
		delete(req.Config, k)
	}

	// Notify all other nodes to create the pool.
	err = notifier(func(client lxd.InstanceServer) error {
		server, _, err := client.GetServer()
		if err != nil {
			return err
		}

		nodeReq := req

		// Merge node specific config items into global config.
		for key, value := range configs[server.Environment.ServerName] {
			nodeReq.Config[key] = value
		}

		err = client.CreateStoragePool(nodeReq)
		if err != nil {
			return err
		}
		logger.Debug("Created storage pool on cluster member", log.Ctx{"pool": req.Name, "member": server.Environment.ServerName})

		return nil
	})
	if err != nil {
		return err
	}

	// Finally update the storage pool state.
	err = d.cluster.Transaction(func(tx *db.ClusterTx) error {
		return tx.StoragePoolCreated(req.Name)
	})
	if err != nil {
		return err
	}
	logger.Debug("Marked storage pool global status as created", log.Ctx{"pool": req.Name})

	return nil
}

// /1.0/storage-pools/{name}
// Get a single storage pool.
func storagePoolGet(d *Daemon, r *http.Request) response.Response {
	// If a target was specified, forward the request to the relevant node.
	resp := forwardedResponseIfTargetIsRemote(d, r)
	if resp != nil {
		return resp
	}

	poolName := mux.Vars(r)["name"]

	// Get the existing storage pool.
	_, pool, _, err := d.cluster.GetStoragePoolInAnyState(poolName)
	if err != nil {
		return response.SmartError(err)
	}

	// Get all users of the storage pool.
	poolUsedBy := []string{}
	err = d.cluster.Transaction(func(tx *db.ClusterTx) error {
		poolUsedBy, err = tx.GetStoragePoolUsedBy(poolName)
		return err
	})
	if err != nil {
		return response.SmartError(err)
	}
	pool.UsedBy = project.FilterUsedBy(r, poolUsedBy)

	targetNode := queryParam(r, "target")

	clustered, err := cluster.Enabled(d.db)
	if err != nil {
		return response.SmartError(err)
	}

	// If no target node is specified and the daemon is clustered, we omit
	// the node-specific fields.
	if targetNode == "" && clustered {
		for _, key := range db.StoragePoolNodeConfigKeys {
			delete(pool.Config, key)
		}
	}

	etag := []interface{}{pool.Name, pool.Driver, pool.Config}

	return response.SyncResponseETag(true, &pool, etag)
}

// /1.0/storage-pools/{name}
// Replace pool properties.
func storagePoolPut(d *Daemon, r *http.Request) response.Response {
	// If a target was specified, forward the request to the relevant node.
	resp := forwardedResponseIfTargetIsRemote(d, r)
	if resp != nil {
		return resp
	}

	poolName := mux.Vars(r)["name"]

	// Get the existing storage pool.
	pool, err := storagePools.GetPoolByName(d.State(), poolName)
	if err != nil {
		return response.SmartError(err)
	}

	targetNode := queryParam(r, "target")
	clustered, err := cluster.Enabled(d.db)
	if err != nil {
		return response.SmartError(err)
	}

	if targetNode == "" && pool.Status() != api.StoragePoolStatusCreated {
		return response.BadRequest(fmt.Errorf("Cannot update storage pool global config when not in created state"))
	}

	// Duplicate config for etag modification and generation.
	etagConfig := util.CopyConfig(pool.Driver().Config())

	// If no target node is specified and the daemon is clustered, we omit the node-specific fields so that
	// the e-tag can be generated correctly. This is because the GET request used to populate the request
	// will also remove node-specific keys when no target is specified.
	if targetNode == "" && clustered {
		for _, key := range db.StoragePoolNodeConfigKeys {
			delete(etagConfig, key)
		}
	}

	// Validate the ETag.
	etag := []interface{}{pool.Name(), pool.Driver().Info().Name, etagConfig}

	err = util.EtagCheck(r, etag)
	if err != nil {
		return response.PreconditionFailed(err)
	}

	// Decode the request.
	req := api.StoragePoolPut{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return response.BadRequest(err)
	}

	// In clustered mode, we differentiate between node specific and non-node specific config keys based on
	// whether the user has specified a target to apply the config to.
	if clustered {
		if targetNode == "" {
			// If no target is specified, then ensure only non-node-specific config keys are changed.
			for k := range req.Config {
				if shared.StringInSlice(k, db.StoragePoolNodeConfigKeys) {
					return response.BadRequest(fmt.Errorf("Config key %q is node-specific", k))
				}
			}
		} else {
			curConfig := pool.Driver().Config()

			// If a target is specified, then ensure only node-specific config keys are changed.
			for k, v := range req.Config {
				if !shared.StringInSlice(k, db.StoragePoolNodeConfigKeys) && curConfig[k] != v {
					return response.BadRequest(fmt.Errorf("Config key %q may not be used as node-specific key", k))
				}
			}
		}
	}

	clientType := request.UserAgentClientType(r.Header.Get("User-Agent"))

	return doStoragePoolUpdate(d, pool, req, targetNode, clientType, r.Method, clustered)
}

// /1.0/storage-pools/{name}
// Change pool properties.
func storagePoolPatch(d *Daemon, r *http.Request) response.Response {
	return storagePoolPut(d, r)
}

// doStoragePoolUpdate takes the current local storage pool config, merges with the requested storage pool config,
// validates and applies the changes. Will also notify other cluster nodes of non-node specific config if needed.
func doStoragePoolUpdate(d *Daemon, pool storagePools.Pool, req api.StoragePoolPut, targetNode string, clientType request.ClientType, httpMethod string, clustered bool) response.Response {
	if req.Config == nil {
		req.Config = map[string]string{}
	}

	// Normally a "put" request will replace all existing config, however when clustered, we need to account
	// for the node specific config keys and not replace them when the request doesn't specify a specific node.
	if targetNode == "" && httpMethod != http.MethodPatch && clustered {
		// If non-node specific config being updated via "put" method in cluster, then merge the current
		// node-specific network config with the submitted config to allow validation.
		// This allows removal of non-node specific keys when they are absent from request config.
		for k, v := range pool.Driver().Config() {
			if shared.StringInSlice(k, db.StoragePoolNodeConfigKeys) {
				req.Config[k] = v
			}
		}
	} else if httpMethod == http.MethodPatch {
		// If config being updated via "patch" method, then merge all existing config with the keys that
		// are present in the request config.
		for k, v := range pool.Driver().Config() {
			_, ok := req.Config[k]
			if !ok {
				req.Config[k] = v
			}
		}
	}

	// Validate the configuration.
	err := storagePoolValidateConfig(pool.Name(), pool.Driver().Info().Name, req.Config, pool.Driver().Config())
	if err != nil {
		return response.BadRequest(err)
	}

	// Notify the other nodes, unless this is itself a notification.
	if clustered && clientType != request.ClientTypeNotifier && targetNode == "" {
		cert := d.endpoints.NetworkCert()
		notifier, err := cluster.NewNotifier(d.State(), cert, cluster.NotifyAll)
		if err != nil {
			return response.SmartError(err)
		}

		sendPool := req
		sendPool.Config = make(map[string]string)
		for k, v := range req.Config {
			// Don't forward node specific keys (these will be merged in on recipient node).
			if shared.StringInSlice(k, db.StoragePoolNodeConfigKeys) {
				continue
			}

			sendPool.Config[k] = v
		}

		err = notifier(func(client lxd.InstanceServer) error {
			return client.UpdateStoragePool(pool.Name(), sendPool, "")
		})
		if err != nil {
			return response.SmartError(err)
		}
	}

	err = pool.Update(clientType, req.Description, req.Config, nil)
	if err != nil {
		return response.InternalError(err)
	}

	return response.EmptySyncResponse
}

// /1.0/storage-pools/{name}
// Delete storage pool.
func storagePoolDelete(d *Daemon, r *http.Request) response.Response {
	poolName := mux.Vars(r)["name"]

	pool, err := storagePools.GetPoolByName(d.State(), poolName)
	if err != nil {
		return response.SmartError(err)
	}

	clientType := request.UserAgentClientType(r.Header.Get("User-Agent"))
	clusterNotification := isClusterNotification(r)
	if !clusterNotification {
		// Sanity checks.
		inUse, err := pool.IsUsed()
		if err != nil {
			return response.SmartError(err)
		}

		if inUse {
			return response.BadRequest(fmt.Errorf("The storage pool is currently in use"))
		}
	}

	// Only delete images if locally stored or running on initial member.
	if !clusterNotification || !pool.Driver().Info().Remote {
		// Only image volumes should remain now.
		volumeNames, err := d.cluster.GetStoragePoolVolumesNames(pool.ID())
		if err != nil {
			return response.InternalError(err)
		}

		for _, volume := range volumeNames {
			_, imgInfo, err := d.cluster.GetImage(projectParam(r), volume, false)
			if err != nil {
				return response.InternalError(errors.Wrapf(err, "Failed getting image info for %q", volume))
			}

			err = doDeleteImageFromPool(d.State(), imgInfo.Fingerprint, pool.Name())
			if err != nil {
				return response.InternalError(errors.Wrapf(err, "Error deleting image %q from pool", volume))
			}
		}
	}

	if pool.LocalStatus() != api.StoragePoolStatusPending {
		err = pool.Delete(clientType, nil)
		if err != nil {
			return response.InternalError(err)
		}
	}

	// If this is a cluster notification, we're done, any database work will be done by the node that is
	// originally serving the request.
	if clusterNotification {
		return response.EmptySyncResponse
	}

	// If we are clustered, also notify all other nodes, if any.
	clustered, err := cluster.Enabled(d.db)
	if err != nil {
		return response.SmartError(err)
	}
	if clustered {
		notifier, err := cluster.NewNotifier(d.State(), d.endpoints.NetworkCert(), cluster.NotifyAll)
		if err != nil {
			return response.SmartError(err)
		}
		err = notifier(func(client lxd.InstanceServer) error {
			_, _, err := client.GetServer()
			if err != nil {
				return err
			}
			return client.DeleteStoragePool(pool.Name())
		})
		if err != nil {
			return response.SmartError(err)
		}
	}

	err = dbStoragePoolDeleteAndUpdateCache(d.State(), pool.Name())
	if err != nil {
		return response.SmartError(err)
	}

	return response.EmptySyncResponse
}
