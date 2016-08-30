package core

import (
	"errors"

	"github.com/supergiant/supergiant/pkg/models"
)

type CloudAccounts struct {
	Collection
}

func (c *CloudAccounts) Create(m *models.CloudAccount) error {
	// NOTE we have to do pre-validation here in order to make sure provider is correct
	if err := validateFields(m); err != nil {
		return err
	}

	if err := c.provider(m).ValidateAccount(m); err != nil {
		return err
	}
	return c.Collection.Create(m)
}

func (c *CloudAccounts) Delete(id *int64, m *models.CloudAccount) error {
	if err := c.core.DB.Find(&m.Kubes, "cloud_account_id = ?", id); err != nil {
		return err
	}
	if len(m.Kubes) > 0 {
		return errors.New("Cannot delete CloudAccount that has active Kubes")
	}
	return c.Collection.Delete(id, m)
}

////////////////////////////////////////////////////////////////////////////////
// Private methods                                                            //
////////////////////////////////////////////////////////////////////////////////

func (c *CloudAccounts) provider(m *models.CloudAccount) Provider {
	switch m.Provider {
	case "aws":
		return &AWSProvider{c.core, m.Credentials}
	default:
		panic("Could not load provider interface for " + m.Provider)
	}
}