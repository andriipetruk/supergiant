package openstack

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"

	"github.com/rackspace/gophercloud"
	"github.com/rackspace/gophercloud/openstack"
	"github.com/rackspace/gophercloud/openstack/compute/v2/extensions/floatingip"
	"github.com/rackspace/gophercloud/openstack/compute/v2/servers"
	"github.com/rackspace/gophercloud/openstack/networking/v2/extensions/external"
	"github.com/rackspace/gophercloud/openstack/networking/v2/extensions/layer3/floatingips"
	"github.com/rackspace/gophercloud/openstack/networking/v2/extensions/layer3/routers"
	"github.com/rackspace/gophercloud/openstack/networking/v2/networks"
	"github.com/rackspace/gophercloud/openstack/networking/v2/subnets"
	"github.com/rackspace/gophercloud/pagination"
	"github.com/supergiant/supergiant/bindata"
	"github.com/supergiant/supergiant/pkg/core"
	"github.com/supergiant/supergiant/pkg/kubernetes"
	"github.com/supergiant/supergiant/pkg/model"
)

// Provider Holds DO account info.
type Provider struct {
	Core   *core.Core
	Client func(*model.Kube) (*gophercloud.ProviderClient, error)
}

// ValidateAccount Valitades Open Stack account info.
func (p *Provider) ValidateAccount(m *model.CloudAccount) error {
	_, err := p.Client(&model.Kube{CloudAccount: m})
	if err != nil {
		return err
	}
	return nil
}

// CreateKube creates a new DO kubernetes cluster.
func (p *Provider) CreateKube(m *model.Kube, action *core.Action) error {

	// Initialize steps
	procedure := &core.Procedure{
		Core:   p.Core,
		Name:   "Create Kube",
		Model:  m,
		Action: action,
	}

	// Method vars
	masterName := m.Name + "-master"
	minionName := m.Name + "-minion"
	// fetch an authenticated provider.
	authenticatedProvider, err := p.Client(m)
	if err != nil {
		return err
	}

	// Fetch compute client.
	computeClient, err := openstack.NewComputeV2(authenticatedProvider, gophercloud.EndpointOpts{
		Region: m.OpenStackConfig.Region,
	})
	if err != nil {
		return err
	}

	// Fetch network client.
	networkClient, err := openstack.NewNetworkV2(authenticatedProvider, gophercloud.EndpointOpts{
		Region: m.OpenStackConfig.Region,
	})
	if err != nil {
		return err
	}

	// Proceedures
	// Network
	procedure.AddStep("Creating Kubernetes Network...", func() error {
		// We specify a name and that it should forward packets
		opts := networks.CreateOpts{
			Name:         m.Name + "-network",
			AdminStateUp: networks.Up,
		}

		// Execute the operation and get back a networks.Network struct
		_, err = networks.Create(networkClient, opts).Extract()
		if err != nil {
			return err
		}
		return nil
	})

	// Subnet
	procedure.AddStep("Creating Kubernetes Subnet...", func() error {
		network, err := getNetwork(networkClient, m)
		if err != nil {
			return err
		}

		opts := subnets.CreateOpts{
			NetworkID: network.ID,
			CIDR:      "192.168.199.0/24",
			IPVersion: subnets.IPv4,
			Name:      m.Name + "-subnet",
		}

		// Execute the operation and get back a subnets.Subnet struct
		_, err = subnets.Create(networkClient, opts).Extract()
		if err != nil {
			return err
		}
		return nil
	})

	// Network
	procedure.AddStep("Creating Kubernetes Router...", func() error {
		externalNet, err := getExternalNetwork(networkClient, m)
		if err != nil {
			return err
		}

		opts := routers.CreateOpts{
			Name:         m.Name + "-router",
			AdminStateUp: networks.Up,
			GatewayInfo: &routers.GatewayInfo{
				NetworkID: externalNet.ID,
			},
		}
		router, err := routers.Create(networkClient, opts).Extract()
		if err != nil {
			return err
		}

		subnet, err := getSubnet(networkClient, m)
		if err != nil {
			return err
		}

		subopts := routers.InterfaceOpts{
			SubnetID: subnet.ID,
		}

		routers.AddInterface(networkClient, router.ID, subopts)

		return nil
	})

	// Master
	procedure.AddStep("Creating Kubernetes Master...", func() error {

		// Build template
		masterUserdataTemplate, err := bindata.Asset("config/providers/openstack/master.yaml")
		if err != nil {
			return err
		}
		masterTemplate, err := template.New("master_template").Parse(string(masterUserdataTemplate))
		if err != nil {
			return err
		}
		var masterUserdata bytes.Buffer
		if err = masterTemplate.Execute(&masterUserdata, m); err != nil {
			return err
		}

		network, err := getNetwork(networkClient, m)
		if err != nil {
			return err
		}

		masterServer, err := servers.Create(computeClient, servers.CreateOpts{
			Name:       masterName,
			FlavorName: m.MasterNodeSize,
			ImageName:  "CoreOS",
			UserData:   masterUserdata.Bytes(),
			Networks: []servers.Network{
				servers.Network{UUID: network.ID},
			},
			Metadata: map[string]string{"kubernetes-cluster": m.Name, "Role": "master"},
		}).Extract()
		if err != nil {
			return err
		}

		m.OpenStackConfig.MasterID = masterServer.ID

		return nil
	})

	// Setup floading IP for master api
	procedure.AddStep("Creating Kubernetes Floating IP...", func() error {
		externalNet, err := getExternalNetwork(networkClient, m)
		if err != nil {
			return err
		}

		floatIP, err := floatingips.Create(networkClient, floatingips.CreateOpts{
			FloatingNetworkID: externalNet.ID,
		}).Extract()
		if err != nil {
			return err
		}

		var masterDevID string
		nodes, err := clusterGather(computeClient, m)
		if err != nil {
			return err
		}

		for _, node := range nodes {
			if node.Name == m.Name+"-master" {
				masterDevID = node.ID
			}
		}

		err = floatingip.AssociateInstance(computeClient, floatingip.AssociateOpts{
			ServerID:   masterDevID,
			FloatingIP: floatIP.FloatingIP,
		}).ExtractErr()
		if err != nil {
			return err
		}

		m.MasterPublicIP = floatIP.FloatingIP

		return nil
	})
	// Minion
	procedure.AddStep("Creating Kubernetes Minion...", func() error {

		network, err := getNetwork(networkClient, m)
		if err != nil {
			return err
		}

		_, err = servers.Create(computeClient, servers.CreateOpts{
			Name:       minionName,
			FlavorName: m.MasterNodeSize, // <- Do we need a minion node size? This will work for now.
			ImageName:  "CoreOS",
			Networks: []servers.Network{
				servers.Network{UUID: network.ID},
			},
			Metadata: map[string]string{"kubernetes-cluster": m.Name, "Role": "minion"},
		}).Extract()
		if err != nil {
			return err
		}
		return nil
	})
	return procedure.Run()
}

// DeleteKube deletes a DO kubernetes cluster.
func (p *Provider) DeleteKube(m *model.Kube, action *core.Action) error {
	// Initialize steps
	procedure := &core.Procedure{
		Core:   p.Core,
		Name:   "Delete Kube",
		Model:  m,
		Action: action,
	}
	// fetch an authenticated provider.
	authenticatedProvider, err := p.Client(m)
	if err != nil {
		return err
	}
	// Fetch compute client.
	client, err := openstack.NewComputeV2(authenticatedProvider, gophercloud.EndpointOpts{
		Region: m.OpenStackConfig.Region,
	})
	if err != nil {
		return err
	}

	// Fetch network client.
	networkClient, err := openstack.NewNetworkV2(authenticatedProvider, gophercloud.EndpointOpts{
		Region: m.OpenStackConfig.Region,
	})
	if err != nil {
		return err
	}

	procedure.AddStep("Destroying kubernetes Floating IP...", func() error {
		floatIP, err := getFloatingIP(networkClient, client, m)
		if err != nil {
			return err
		}

		err = floatingip.Delete(networkClient, floatIP.ID).ExtractErr()
		if err != nil {
			if strings.Contains(err.Error(), "404") {
				// it does not exist,
				return nil
			}
			return err
		}

		return nil
	})

	procedure.AddStep("Destroying kubernetes nodes...", func() error {
		// Go find all our cluster members.
		kube, err := clusterGather(client, m)
		if err != nil {
			return err
		}

		for _, s := range kube {
			result := servers.Delete(client, s.ID)
			err = result.ExtractErr()
			if err != nil {
				return err
			}
		}
		return nil
	})

	procedure.AddStep("Destroying kubernetes Router...", func() error {
		router, err := getRouter(networkClient, m)
		if err != nil {
			return err
		}

		subnet, err := getSubnet(networkClient, m)
		if err != nil {
			return err
		}

		subopts := routers.InterfaceOpts{
			SubnetID: subnet.ID,
		}

		_, err = routers.RemoveInterface(networkClient, router.ID, subopts).Extract()
		if err != nil {
			return err
		}

		result := routers.Delete(networkClient, router.ID)
		err = result.ExtractErr()
		if err != nil {
			if strings.Contains(err.Error(), "404") {
				// it does not exist,
				return nil
			}
			return err
		}

		return nil
	})

	procedure.AddStep("Destroying kubernetes network...", func() error {
		// Find our network
		network, err := getNetwork(networkClient, m)
		if err != nil {
			return err
		}

		result := networks.Delete(networkClient, network.ID)
		err = result.ExtractErr()
		if err != nil {
			if strings.Contains(err.Error(), "404") {
				// it does not exist,
				return nil
			}
			return err
		}

		return nil
	})

	return procedure.Run()
}

// CreateNode creates a new minion on DO kubernetes cluster.
func (p *Provider) CreateNode(m *model.Node, action *core.Action) error {
	return nil
}

// DeleteNode deletes a minsion on a DO kubernetes cluster.
func (p *Provider) DeleteNode(m *model.Node, action *core.Action) error {

	return nil
}

// CreateVolume createss a Volume on DO for Kubernetes
func (p *Provider) CreateVolume(m *model.Volume, action *core.Action) error {

	return nil
}

func (p *Provider) KubernetesVolumeDefinition(m *model.Volume) *kubernetes.Volume {
	return &kubernetes.Volume{
		Name: m.Name,
		FlexVolume: &kubernetes.FlexVolume{
			Driver: "supergiant.io/digitalocean",
			FSType: "ext4",
			Options: map[string]string{
				"volumeID": m.ProviderID,
				"name":     m.Name,
			},
		},
	}
}

// ResizeVolume re-sizes volume on DO kubernetes cluster.
func (p *Provider) ResizeVolume(m *model.Volume, action *core.Action) error {

	return nil
}

// WaitForVolumeAvailable waits for DO volume to become available.
func (p *Provider) WaitForVolumeAvailable(m *model.Volume, action *core.Action) error {
	return nil
}

// DeleteVolume deletes a DO volume.
func (p *Provider) DeleteVolume(m *model.Volume, action *core.Action) error {

	return nil
}

// CreateEntrypoint creates a new Load Balancer for Kubernetes in DO
func (p *Provider) CreateEntrypoint(m *model.Entrypoint, action *core.Action) error {
	return nil
}

// DeleteEntrypoint deletes load balancer from DO.
func (p *Provider) DeleteEntrypoint(m *model.Entrypoint, action *core.Action) error {
	return nil
}

func (p *Provider) CreateEntrypointListener(m *model.EntrypointListener, action *core.Action) error {
	return nil
}

func (p *Provider) DeleteEntrypointListener(m *model.EntrypointListener, action *core.Action) error {
	return nil
}

////////////////////////////////////////////////////////////////////////////////
// Private methods                                                            //
////////////////////////////////////////////////////////////////////////////////

// Client creates the client for the provider.
func Client(kube *model.Kube) (*gophercloud.ProviderClient, error) {
	opts := gophercloud.AuthOptions{
		IdentityEndpoint: kube.CloudAccount.Credentials["identity_endpoint"],
		Username:         kube.CloudAccount.Credentials["username"],
		Password:         kube.CloudAccount.Credentials["password"],
		TenantID:         kube.CloudAccount.Credentials["tenant_id"],
	}

	client, err := openstack.AuthenticatedClient(opts)
	if err != nil {
		return nil, err
	}

	return client, nil
}

// Gather all cluster members.
func clusterGather(client *gophercloud.ServiceClient, m *model.Kube) ([]servers.Server, error) {
	opts := servers.ListOpts{Name: ""}
	pager := servers.List(client, opts)

	// In this section we gather up all members of our cluster into this slice.
	var kube []servers.Server
	// Define an anonymous function to be executed on each page's iteration
	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		serverList, err := servers.ExtractServers(page)
		if err != nil {
			return false, err
		}

		for _, s := range serverList {
			for key, value := range s.Metadata {
				if key == "kubernetes-cluster" && value == m.Name {
					kube = append(kube, s)
				}
			}
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return kube, nil
}
func getSubnet(client *gophercloud.ServiceClient, m *model.Kube) (subnets.Subnet, error) {
	var subnet subnets.Subnet
	pager := subnets.List(client, subnets.ListOpts{Name: ""})
	pager.EachPage(func(page pagination.Page) (bool, error) {
		nets, err := subnets.ExtractSubnets(page)
		if err != nil {
			return false, err
		}

		for _, s := range nets {
			if s.Name == m.Name+"-subnet" {
				subnet = s
			}
		}
		return false, nil
	})
	return subnet, nil
}

func getNetwork(client *gophercloud.ServiceClient, m *model.Kube) (networks.Network, error) {
	var network networks.Network
	pager := networks.List(client, networks.ListOpts{Name: ""})
	pager.EachPage(func(page pagination.Page) (bool, error) {
		networks, err := networks.ExtractNetworks(page)
		if err != nil {
			return false, err
		}

		for _, n := range networks {
			if n.Name == m.Name+"-network" {
				network = n
			}
		}
		return false, nil
	})
	return network, nil
}

func getExternalNetwork(client *gophercloud.ServiceClient, m *model.Kube) (external.NetworkExternal, error) {
	var externalNetwork external.NetworkExternal
	pager := networks.List(client, networks.ListOpts{Name: ""})
	pager.EachPage(func(page pagination.Page) (bool, error) {
		externalNetworks, err := external.ExtractList(page)
		if err != nil {
			return false, err
		}

		for _, e := range externalNetworks {
			if e.Name == "public" {
				externalNetwork = e
			}
		}
		return false, nil
	})
	return externalNetwork, nil
}

func getRouter(client *gophercloud.ServiceClient, m *model.Kube) (routers.Router, error) {
	var router routers.Router
	pager := routers.List(client, routers.ListOpts{Name: ""})
	pager.EachPage(func(page pagination.Page) (bool, error) {
		routers, err := routers.ExtractRouters(page)
		if err != nil {
			return false, err
		}

		for _, r := range routers {
			if r.Name == m.Name+"-router" {
				router = r
			}
		}
		return false, nil
	})
	return router, nil
}

func getFloatingIP(client *gophercloud.ServiceClient, serviceClient *gophercloud.ServiceClient, m *model.Kube) (floatingips.FloatingIP, error) {
	var floatingIP floatingips.FloatingIP
	pager := floatingips.List(client, floatingips.ListOpts{})
	pager.EachPage(func(page pagination.Page) (bool, error) {
		floatingIPs, err := floatingips.ExtractFloatingIPs(page)
		if err != nil {
			return false, err
		}

		for _, f := range floatingIPs {
			fmt.Println("FLOAT:", f.FloatingIP, "MASTER:", m.MasterPublicIP)
			if f.FloatingIP == m.MasterPublicIP {
				floatingIP = f
			}
		}
		return false, nil
	})
	return floatingIP, nil
}
