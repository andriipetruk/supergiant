package openstack

import (
	"github.com/rackspace/gophercloud"
	"github.com/rackspace/gophercloud/openstack"
	"github.com/rackspace/gophercloud/openstack/compute/v2/servers"
	"github.com/rackspace/gophercloud/pagination"
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
	client, err := openstack.NewComputeV2(authenticatedProvider, gophercloud.EndpointOpts{
		Region: m.OpenStackConfig.Region,
	})

	// Proceedures
	// Master
	procedure.AddStep("Creating Kubernetes Master...", func() error {
		_, err = servers.Create(client, servers.CreateOpts{
			Name:       masterName,
			FlavorName: m.MasterNodeSize,
			ImageName:  "CoreOS",
			Metadata:   map[string]string{"kubernetes-cluster": m.Name, "Role": "master"},
		}).Extract()
		if err != nil {
			return err
		}
		return nil
	})
	// Minion
	procedure.AddStep("Creating Kubernetes Minion...", func() error {
		_, err = servers.Create(client, servers.CreateOpts{
			Name:       minionName,
			FlavorName: m.MasterNodeSize, // <- Do we need a minion node size? This will work for now.
			ImageName:  "CoreOS",
			Metadata:   map[string]string{"kubernetes-cluster": m.Name, "Role": "minion"},
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
	// fetch an authenticated provider.
	authenticatedProvider, err := p.Client(m)
	if err != nil {
		return err
	}
	// Fetch compute client.
	client, err := openstack.NewComputeV2(authenticatedProvider, gophercloud.EndpointOpts{
		Region: m.OpenStackConfig.Region,
	})
	opts := servers.ListOpts{Name: ""}
	pager := servers.List(client, opts)

	var kube []servers.Server
	// Define an anonymous function to be executed on each page's iteration
	err = pager.EachPage(func(page pagination.Page) (bool, error) {
		err := err
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
		return err
	}

	for _, s := range kube {
		_ = servers.Delete(client, s.ID)
	}

	return nil
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
