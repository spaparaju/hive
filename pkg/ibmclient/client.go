package ibmclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/networking-go-sdk/dnsrecordsv1"
	"github.com/IBM/networking-go-sdk/zonesv1"
	"github.com/IBM/platform-services-go-sdk/iamidentityv1"
	"github.com/IBM/platform-services-go-sdk/resourcecontrollerv2"
	"github.com/IBM/platform-services-go-sdk/resourcemanagerv2"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/openshift/hive/pkg/constants"
	"github.com/pkg/errors"

	corev1 "k8s.io/api/core/v1"
)

//go:generate mockgen -source=./client.go -destination=./mock/client_generated.go -package=mock

// API represents the calls made to the API.
type API interface {
	GetAuthenticatorAPIKeyDetails(ctx context.Context) (*iamidentityv1.APIKey, error)
	GetCISInstance(ctx context.Context, crnstr string) (*resourcecontrollerv2.ResourceInstance, error)
	GetDedicatedHostByName(ctx context.Context, name string, region string) (*vpcv1.DedicatedHost, error)
	GetDedicatedHostProfiles(ctx context.Context, region string) ([]vpcv1.DedicatedHostProfile, error)
	GetDNSRecordsByName(ctx context.Context, crnstr string, zoneID string, recordName string) ([]dnsrecordsv1.DnsrecordDetails, error)
	GetDNSZoneIDByName(ctx context.Context, name string) (string, error)
	GetDNSZones(ctx context.Context) ([]DNSZoneResponse, error)
	GetEncryptionKey(ctx context.Context, keyCRN string) (*EncryptionKeyResponse, error)
	GetResourceGroups(ctx context.Context) ([]resourcemanagerv2.ResourceGroup, error)
	GetResourceGroup(ctx context.Context, nameOrID string) (*resourcemanagerv2.ResourceGroup, error)
	GetSubnet(ctx context.Context, subnetID string) (*vpcv1.Subnet, error)
	GetVSIProfiles(ctx context.Context) ([]vpcv1.InstanceProfile, error)
	GetVPC(ctx context.Context, vpcID string) (*vpcv1.VPC, error)
	GetVPCZonesForRegion(ctx context.Context, region string) ([]string, error)
	GetVPCInstances(ctx context.Context, resourceGroupID string) ([]vpcv1.Instance, error)
	StartInstances(instances []vpcv1.Instance) error
	StopInstances(instances []vpcv1.Instance) error
}

// Client makes calls to the IBM Cloud API.
type Client struct {
	managementAPI *resourcemanagerv2.ResourceManagerV2
	controllerAPI *resourcecontrollerv2.ResourceControllerV2
	vpcAPI        *vpcv1.VpcV1
	Authenticator *core.IamAuthenticator
}

// cisServiceID is the Cloud Internet Services' catalog service ID.
const cisServiceID = "75874a60-cb12-11e7-948e-37ac098eb1b9"

// VPCResourceNotFoundError represents an error for a VPC resoruce that is not found.
type VPCResourceNotFoundError struct{}

// Error returns the error message for the VPCResourceNotFoundError error type.
func (e *VPCResourceNotFoundError) Error() string {
	return "Not Found"
}

// DNSZoneResponse represents a DNS zone response.
type DNSZoneResponse struct {
	// Name is the domain name of the zone.
	Name string

	// ID is the zone's ID.
	ID string

	// CISInstanceCRN is the IBM Cloud Resource Name for the CIS instance where
	// the DNS zone is managed.
	CISInstanceCRN string

	// CISInstanceName is the display name of the CIS instance where the DNS zone
	// is managed.
	CISInstanceName string

	// ResourceGroupID is the resource group ID of the CIS instance.
	ResourceGroupID string
}

// EncryptionKeyResponse represents an encryption key response.
type EncryptionKeyResponse struct{}

// NewClient initializes a client with a session.
func NewClient(apiKey string) (*Client, error) {
	authenticator := &core.IamAuthenticator{
		ApiKey: apiKey,
	}

	client := &Client{
		Authenticator: authenticator,
	}

	if err := client.loadSDKServices(); err != nil {
		return nil, errors.Wrap(err, "failed to load IBM SDK services")
	}

	return client, nil
}

func NewClientFromSecret(secret *corev1.Secret) (*Client, error) {
	apiKey, ok := secret.Data[constants.IBMCloudAPIKeySecretKey]
	if !ok {
		return nil, errors.New("creds secret does not contain \"" + constants.IBMCloudAPIKeySecretKey + "\" data")
	}
	return NewClient(string(apiKey))
}

func (c *Client) loadSDKServices() error {
	servicesToLoad := []func() error{
		c.loadResourceManagementAPI,
		c.loadResourceControllerAPI,
		c.loadVPCV1API,
	}

	// Call all the load functions.
	for _, fn := range servicesToLoad {
		if err := fn(); err != nil {
			return err
		}
	}

	return nil
}

// GetAuthenticatorAPIKeyDetails gets detailed information on the API key used
// for authentication to the IBM Cloud APIs
func (c *Client) GetAuthenticatorAPIKeyDetails(ctx context.Context) (*iamidentityv1.APIKey, error) {
	iamIdentityService, err := iamidentityv1.NewIamIdentityV1(&iamidentityv1.IamIdentityV1Options{
		Authenticator: c.Authenticator,
	})
	if err != nil {
		return nil, err
	}

	options := iamIdentityService.NewGetAPIKeysDetailsOptions()
	options.SetIamAPIKey(c.Authenticator.ApiKey)
	details, _, err := iamIdentityService.GetAPIKeysDetailsWithContext(ctx, options)
	if err != nil {
		return nil, err
	}
	return details, nil
}

// GetCISInstance gets a specific Cloud Internet Services instance by its CRN.
func (c *Client) GetCISInstance(ctx context.Context, crnstr string) (*resourcecontrollerv2.ResourceInstance, error) {
	_, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	options := c.controllerAPI.NewGetResourceInstanceOptions(crnstr)
	resourceInstance, _, err := c.controllerAPI.GetResourceInstance(options)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cis instances")
	}

	return resourceInstance, nil
}

// GetDedicatedHostByName gets dedicated host by name.
func (c *Client) GetDedicatedHostByName(ctx context.Context, name string, region string) (*vpcv1.DedicatedHost, error) {
	err := c.setVPCServiceURLForRegion(ctx, region)
	if err != nil {
		return nil, err
	}

	options := c.vpcAPI.NewListDedicatedHostsOptions()
	dhosts, _, err := c.vpcAPI.ListDedicatedHostsWithContext(ctx, options)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list dedicated hosts")
	}

	for _, dhost := range dhosts.DedicatedHosts {
		if *dhost.Name == name {
			return &dhost, nil
		}
	}

	return nil, fmt.Errorf("dedicated host %q not found", name)
}

// GetDedicatedHostProfiles gets a list of profiles supported in a region.
func (c *Client) GetDedicatedHostProfiles(ctx context.Context, region string) ([]vpcv1.DedicatedHostProfile, error) {
	err := c.setVPCServiceURLForRegion(ctx, region)
	if err != nil {
		return nil, err
	}

	profilesOptions := c.vpcAPI.NewListDedicatedHostProfilesOptions()
	profiles, _, err := c.vpcAPI.ListDedicatedHostProfilesWithContext(ctx, profilesOptions)
	if err != nil {
		return nil, err
	}

	return profiles.Profiles, nil
}

// GetDNSRecordsByName gets DNS records in specific Cloud Internet Services instance
// by its CRN, zone ID, and DNS record name.
func (c *Client) GetDNSRecordsByName(ctx context.Context, crnstr string, zoneID string, recordName string) ([]dnsrecordsv1.DnsrecordDetails, error) {
	// Set CIS DNS record service
	dnsService, err := dnsrecordsv1.NewDnsRecordsV1(&dnsrecordsv1.DnsRecordsV1Options{
		Authenticator:  c.Authenticator,
		Crn:            core.StringPtr(crnstr),
		ZoneIdentifier: core.StringPtr(zoneID),
	})
	if err != nil {
		return nil, err
	}

	// Get CIS DNS records by name
	records, _, err := dnsService.ListAllDnsRecordsWithContext(ctx, &dnsrecordsv1.ListAllDnsRecordsOptions{
		Name: core.StringPtr(recordName),
	})
	if err != nil {
		return nil, errors.Wrap(err, "could not retrieve DNS records")
	}

	return records.Result, nil
}

// GetDNSZoneIDByName gets the CIS zone ID from its domain name.
func (c *Client) GetDNSZoneIDByName(ctx context.Context, name string) (string, error) {

	zones, err := c.GetDNSZones(ctx)
	if err != nil {
		return "", err
	}

	for _, z := range zones {
		if z.Name == name {
			return z.ID, nil
		}
	}

	return "", fmt.Errorf("DNS zone %q not found", name)
}

// GetDNSZones returns all of the active DNS zones managed by CIS.
func (c *Client) GetDNSZones(ctx context.Context) ([]DNSZoneResponse, error) {
	_, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	options := c.controllerAPI.NewListResourceInstancesOptions()
	options.SetResourceID(cisServiceID)

	listResourceInstancesResponse, _, err := c.controllerAPI.ListResourceInstances(options)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cis instance")
	}

	var allZones []DNSZoneResponse
	for _, instance := range listResourceInstancesResponse.Resources {
		crnstr := instance.CRN
		zonesService, err := zonesv1.NewZonesV1(&zonesv1.ZonesV1Options{
			Authenticator: c.Authenticator,
			Crn:           crnstr,
		})
		if err != nil {
			return nil, errors.Wrap(err, "failed to list DNS zones")
		}

		options := zonesService.NewListZonesOptions()
		listZonesResponse, _, err := zonesService.ListZones(options)

		if listZonesResponse == nil {
			return nil, err
		}

		for _, zone := range listZonesResponse.Result {
			if *zone.Status == "active" {
				zoneStruct := DNSZoneResponse{
					Name:            *zone.Name,
					ID:              *zone.ID,
					CISInstanceCRN:  *instance.CRN,
					CISInstanceName: *instance.Name,
					ResourceGroupID: *instance.ResourceGroupID,
				}
				allZones = append(allZones, zoneStruct)
			}
		}
	}

	return allZones, nil
}

// GetEncryptionKey gets data for an encryption key
func (c *Client) GetEncryptionKey(ctx context.Context, keyCRN string) (*EncryptionKeyResponse, error) {
	// TODO: IBM: Call KMS / Hyperprotect Crpyto APIs.
	return &EncryptionKeyResponse{}, nil
}

// GetResourceGroup gets a resource group by its name or ID.
func (c *Client) GetResourceGroup(ctx context.Context, nameOrID string) (*resourcemanagerv2.ResourceGroup, error) {
	_, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	groups, err := c.GetResourceGroups(ctx)
	if err != nil {
		return nil, err
	}

	for idx, rg := range groups {
		if *rg.ID == nameOrID || *rg.Name == nameOrID {
			return &groups[idx], nil
		}
	}
	return nil, fmt.Errorf("resource group %q not found", nameOrID)
}

// GetResourceGroups gets the list of resource groups.
func (c *Client) GetResourceGroups(ctx context.Context) ([]resourcemanagerv2.ResourceGroup, error) {
	_, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	apikey, err := c.GetAuthenticatorAPIKeyDetails(ctx)
	if err != nil {
		return nil, err
	}

	options := c.managementAPI.NewListResourceGroupsOptions()
	options.SetAccountID(*apikey.AccountID)
	listResourceGroupsResponse, _, err := c.managementAPI.ListResourceGroupsWithContext(ctx, options)
	if err != nil {
		return nil, err
	}
	return listResourceGroupsResponse.Resources, nil
}

// GetSubnet gets a subnet by its ID.
func (c *Client) GetSubnet(ctx context.Context, subnetID string) (*vpcv1.Subnet, error) {
	_, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	subnet, detailedResponse, err := c.vpcAPI.GetSubnet(&vpcv1.GetSubnetOptions{ID: &subnetID})
	if detailedResponse.GetStatusCode() == http.StatusNotFound {
		return nil, &VPCResourceNotFoundError{}
	}
	return subnet, err
}

// GetVSIProfiles gets a list of all VSI profiles.
func (c *Client) GetVSIProfiles(ctx context.Context) ([]vpcv1.InstanceProfile, error) {
	listInstanceProfilesOptions := c.vpcAPI.NewListInstanceProfilesOptions()
	profiles, _, err := c.vpcAPI.ListInstanceProfilesWithContext(ctx, listInstanceProfilesOptions)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list vpc vsi profiles")
	}
	return profiles.Profiles, nil
}

// GetVPC gets a VPC by its ID.
func (c *Client) GetVPC(ctx context.Context, vpcID string) (*vpcv1.VPC, error) {
	_, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	regions, err := c.getVPCRegions(ctx)
	if err != nil {
		return nil, err
	}

	for _, region := range regions {
		err := c.vpcAPI.SetServiceURL(fmt.Sprintf("%s/v1", *region.Endpoint))
		if err != nil {
			return nil, errors.Wrap(err, "failed to set vpc api service url")
		}

		if vpc, detailedResponse, err := c.vpcAPI.GetVPC(c.vpcAPI.NewGetVPCOptions(vpcID)); err != nil {
			if detailedResponse.GetStatusCode() != http.StatusNotFound {
				return nil, err
			}
		} else if vpc != nil {
			return vpc, nil
		}
	}

	return nil, &VPCResourceNotFoundError{}
}

// GetVPCZonesForRegion gets the supported zones for a VPC region.
func (c *Client) GetVPCZonesForRegion(ctx context.Context, region string) ([]string, error) {
	_, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	regionZonesOptions := c.vpcAPI.NewListRegionZonesOptions(region)
	zones, _, err := c.vpcAPI.ListRegionZonesWithContext(ctx, regionZonesOptions)
	if err != nil {
		return nil, err
	}

	response := make([]string, len(zones.Zones))
	for idx, zone := range zones.Zones {
		response[idx] = *zone.Name
	}
	return response, err
}

func (c *Client) getVPCRegions(ctx context.Context) ([]vpcv1.Region, error) {
	listRegionsOptions := c.vpcAPI.NewListRegionsOptions()
	listRegionsResponse, _, err := c.vpcAPI.ListRegionsWithContext(ctx, listRegionsOptions)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list vpc regions")
	}

	return listRegionsResponse.Regions, nil
}

func (c *Client) loadResourceManagementAPI() error {
	options := &resourcemanagerv2.ResourceManagerV2Options{
		Authenticator: c.Authenticator,
	}
	resourceManagerV2Service, err := resourcemanagerv2.NewResourceManagerV2(options)
	if err != nil {
		return err
	}
	c.managementAPI = resourceManagerV2Service
	return nil
}

func (c *Client) loadResourceControllerAPI() error {
	options := &resourcecontrollerv2.ResourceControllerV2Options{
		Authenticator: c.Authenticator,
	}
	resourceControllerV2Service, err := resourcecontrollerv2.NewResourceControllerV2(options)
	if err != nil {
		return err
	}
	c.controllerAPI = resourceControllerV2Service
	return nil
}

func (c *Client) loadVPCV1API() error {
	vpcService, err := vpcv1.NewVpcV1(&vpcv1.VpcV1Options{
		Authenticator: c.Authenticator,
	})
	if err != nil {
		return err
	}
	c.vpcAPI = vpcService
	return nil
}

func (c *Client) setVPCServiceURLForRegion(ctx context.Context, region string) error {
	regionOptions := c.vpcAPI.NewGetRegionOptions(region)
	vpcRegion, _, err := c.vpcAPI.GetRegionWithContext(ctx, regionOptions)
	if err != nil {
		return err
	}
	err = c.vpcAPI.SetServiceURL(fmt.Sprintf("%s/v1", *vpcRegion.Endpoint))
	if err != nil {
		return err
	}
	return nil
}

func GetCISInstanceCRN(client API, ctx context.Context, baseDomain string) (string, error) {
	zones, err := client.GetDNSZones(ctx)
	if err != nil {
		return "", errors.Wrap(err, "Unable to retrieve IBM Cloud DNS zones")
	}
	cisInstanceCRN := ""
	for _, z := range zones {
		if z.Name == baseDomain {
			cisInstanceCRN = z.CISInstanceCRN
			break
		}
	}
	if cisInstanceCRN == "" {
		return "", errors.Errorf("Could not determine CISInstanceCRN, no DNS zone found for base domain %s", baseDomain)
	}
	return cisInstanceCRN, nil
}

func GetAccountID(client API, ctx context.Context) (string, error) {
	apiKeyDetails, err := client.GetAuthenticatorAPIKeyDetails(context.TODO())
	if err != nil {
		return "", errors.Wrap(err, "Unable to retrieve IBM Cloud APIKeyDetails")
	}
	return *apiKeyDetails.AccountID, nil
}

func (c *Client) GetVPCInstances(ctx context.Context, infraID string) ([]vpcv1.Instance, error) {
	options := &vpcv1.ListInstancesOptions{}
	options.SetVPCName(fmt.Sprintf("%s-vpc", infraID))
	result, _, err := c.vpcAPI.ListInstances(options)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list vpc instances")
	}
	var instances []vpcv1.Instance
	for idx, instance := range result.Instances {
		if strings.Contains(*instance.Name, infraID) {
			instances = append(instances, result.Instances[idx])
		}
	}
	return instances, nil
}

func (c *Client) StopInstances(instances []vpcv1.Instance) error {
	for _, instance := range instances {
		options := &vpcv1.CreateInstanceActionOptions{}
		options.SetInstanceID(*instance.ID)
		options.SetType(vpcv1.CreateInstanceActionOptionsTypeStopConst)
		_, _, err := c.vpcAPI.CreateInstanceAction(options)
		if err != nil {
			return errors.Wrap(err, "failed to create stop instance action")
		}
	}
	return nil
}

func (c *Client) StartInstances(instances []vpcv1.Instance) error {
	for _, instance := range instances {
		options := &vpcv1.CreateInstanceActionOptions{}
		options.SetInstanceID(*instance.ID)
		options.SetType(vpcv1.CreateInstanceActionOptionsTypeStartConst)
		_, _, err := c.vpcAPI.CreateInstanceAction(options)
		if err != nil {
			return errors.Wrapf(err, "failed to create start instance action for instance %q", *instance.Name)
		}
	}
	return nil
}
