package hibernation

import (
	"context"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hivev1 "github.com/openshift/hive/apis/hive/v1"
	ibmclient "github.com/openshift/hive/pkg/ibmclient"
)

var (
	// States described in IBM Cloud API docs
	// https://cloud.ibm.com/apidocs/vpc?code=go#get-instance
	ibmRunningStates           = sets.NewString("running")
	ibmStoppedStates           = sets.NewString("stopped")
	ibmPendingStates           = sets.NewString("pending")
	ibmStoppingStates          = sets.NewString("stopping")
	ibmRunningOrPendingStates  = ibmRunningStates.Union(ibmPendingStates)
	ibmStoppedOrStoppingStates = ibmStoppedStates.Union(ibmStoppingStates)
	ibmNotRunningStates        = ibmStoppedOrStoppingStates.Union(ibmPendingStates)
	ibmNotStoppedStates        = ibmRunningOrPendingStates.Union(ibmStoppingStates)
)

func init() {
	RegisterActuator(&ibmCloudActuator{ibmCloudClientFn: getIBMCloudClient})
}

type ibmCloudActuator struct {
	// ibmCloudClientFn is the function to build an IBM Cloud client, here for testing
	ibmCloudClientFn func(*hivev1.ClusterDeployment, client.Client, log.FieldLogger) (ibmclient.API, error)
}

// CanHandle returns true if the actuator can handle a particular ClusterDeployment
func (a *ibmCloudActuator) CanHandle(cd *hivev1.ClusterDeployment) bool {
	return cd.Spec.Platform.IBMCloud != nil
}

// StopMachines will stop machines belonging to the given ClusterDeployment
func (a *ibmCloudActuator) StopMachines(cd *hivev1.ClusterDeployment, hiveClient client.Client, logger log.FieldLogger) error {
	logger = logger.WithField("cloud", "ibmcloud")
	ibmCloudClient, err := a.ibmCloudClientFn(cd, hiveClient, logger)
	if err != nil {
		return err
	}

	instances, err := getIBMCloudClusterInstances(cd, ibmCloudClient, runningOrPendingStates, logger)
	if err != nil {
		return err
	}
	if len(instances) == 0 {
		logger.Info("No instances were found to stop")
		return nil
	}
	err = ibmCloudClient.StopInstances(instances)
	if err != nil {
		logger.WithError(err).Error("failed to stop IBM Cloud instances")
		return err
	}

	return nil
}

// StartMachines will start machines belonging to the given ClusterDeployment
func (a *ibmCloudActuator) StartMachines(cd *hivev1.ClusterDeployment, hiveClient client.Client, logger log.FieldLogger) error {
	logger = logger.WithField("cloud", "ibmcloud")
	ibmCloudClient, err := a.ibmCloudClientFn(cd, hiveClient, logger)
	if err != nil {
		return err
	}

	instances, err := getIBMCloudClusterInstances(cd, ibmCloudClient, stoppedOrStoppingStates, logger)
	if err != nil {
		return err
	}
	if len(instances) == 0 {
		logger.Info("No instances were found to start")
		return nil
	}
	err = ibmCloudClient.StartInstances(instances)
	if err != nil {
		logger.WithError(err).Error("failed to start IBM Cloud instances")
		return err
	}

	return nil
}

func ibmCloudInstanceNames(instances []vpcv1.Instance) []string {
	names := make([]string, len(instances))
	for i, instance := range instances {
		names[i] = *instance.Name
	}
	return names
}

// MachinesRunning will return true if the machines associated with the given
// ClusterDeployment are in a running state. It also returns a list of machines that
// are not running.
func (a *ibmCloudActuator) MachinesRunning(cd *hivev1.ClusterDeployment, hiveClient client.Client, logger log.FieldLogger) (bool, []string, error) {
	logger = logger.WithField("cloud", "ibmcloud")
	logger.Infof("checking whether machines are running")
	ibmCloudClient, err := a.ibmCloudClientFn(cd, hiveClient, logger)
	if err != nil {
		return false, nil, err
	}
	instances, err := getIBMCloudClusterInstances(cd, ibmCloudClient, notRunningStates, logger)
	if err != nil {
		return false, nil, err
	}
	return len(instances) == 0, ibmCloudInstanceNames(instances), nil
}

// MachinesStopped will return true if the machines associated with the given
// ClusterDeployment are in a stopped state. It also returns a list of machines
// that have not stopped.
func (a *ibmCloudActuator) MachinesStopped(cd *hivev1.ClusterDeployment, hiveClient client.Client, logger log.FieldLogger) (bool, []string, error) {
	logger = logger.WithField("cloud", "ibmcloud")
	logger.Infof("checking whether machines are stopped")
	ibmCloudClient, err := a.ibmCloudClientFn(cd, hiveClient, logger)
	if err != nil {
		return false, nil, err
	}
	instances, err := getIBMCloudClusterInstances(cd, ibmCloudClient, notStoppedStates, logger)
	if err != nil {
		return false, nil, err
	}
	return len(instances) == 0, ibmCloudInstanceNames(instances), nil
}

func getIBMCloudClient(cd *hivev1.ClusterDeployment, c client.Client, logger log.FieldLogger) (ibmclient.API, error) {
	secret := &corev1.Secret{}
	err := c.Get(context.TODO(), client.ObjectKey{Name: cd.Spec.Platform.IBMCloud.CredentialsSecretRef.Name, Namespace: cd.Namespace}, secret)
	if err != nil {
		logger.WithError(err).Error("failed to fetch IBM Cloud credentials secret")
		return nil, errors.Wrap(err, "failed to fetch IBM Cloud credentials secret")
	}
	return ibmclient.NewClientFromSecret(secret)
}

func getIBMCloudClusterInstances(cd *hivev1.ClusterDeployment, c ibmclient.API, states sets.String, logger log.FieldLogger) ([]vpcv1.Instance, error) {
	infraID := cd.Spec.ClusterMetadata.InfraID
	logger = logger.WithField("infraID", infraID)
	logger.Debug("listing cluster instances")

	instances, err := c.GetVPCInstances(context.TODO(), infraID)
	if err != nil {
		logger.WithError(err).Error("failed to list instances")
		return nil, err
	}
	var result []vpcv1.Instance
	for idx, i := range instances {
		if states.Has(*i.Status) {
			result = append(result, instances[idx])
		}
	}
	logger.WithField("count", len(result)).WithField("states", states).Debug("result of listing instances")
	return result, nil
}
