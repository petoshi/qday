package goupnp

import (
	"context"
	"fmt"
	"net/url"
)

type GetSpecificPortMappingEntryRequest struct {
	NewRemoteHost   string
	NewExternalPort uint16
	NewProtocol     string
}

type GetSpecificPortMappingEntryResponse struct {
	NewInternalPort           uint16
	NewInternalClient         string
	NewEnabled                bool
	NewPortMappingDescription string
	NewLeaseDuration          string
}

type AddPortMappingRequest struct {
	NewRemoteHost             string
	NewExternalPort           uint16
	NewProtocol               string
	NewInternalPort           uint16
	NewInternalClient         string
	NewEnabled                bool
	NewPortMappingDescription string
	NewLeaseDuration          uint32
}

type DeletePortMappingRequest struct {
	NewRemoteHost   string
	NewExternalPort uint16
	NewProtocol     string
}

type GetExternalIPAddressResponse struct {
	NewExternalIPAddress string
}

type IGDClient struct {
	ctx     context.Context
	urlBase string
	srv     Service
}

func (igd IGDClient) performAction(actionName string, req interface{}, resp interface{}) error {
	ctx := igd.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	base, err := url.Parse(igd.urlBase)
	if err != nil {
		return err
	}
	control, err := url.Parse(igd.srv.ControlURL)
	if err != nil {
		return err
	}
	endpoint := base.ResolveReference(control)
	if endpoint.Hostname() != base.Hostname() || endpoint.Scheme != "http" {
		return fmt.Errorf("UPnP control URL is outside the discovered router")
	}
	return performSOAPAction(ctx, endpoint.String(), igd.srv.ServiceType, actionName, req, resp)
}

func (igd IGDClient) GetSpecificPortMappingEntry(req GetSpecificPortMappingEntryRequest) (resp GetSpecificPortMappingEntryResponse, err error) {
	err = igd.performAction("GetSpecificPortMappingEntry", req, &resp)
	return
}

func (igd IGDClient) AddPortMapping(req AddPortMappingRequest) error {
	return igd.performAction("AddPortMapping", req, nil)
}

func (igd IGDClient) DeletePortMapping(req DeletePortMappingRequest) error {
	return igd.performAction("DeletePortMapping", req, nil)
}

func (igd IGDClient) GetExternalIPAddress() (resp GetExternalIPAddressResponse, err error) {
	err = igd.performAction("GetExternalIPAddress", nil, &resp)
	return
}

func (igd IGDClient) Location() string {
	return igd.urlBase
}

func (igd IGDClient) ServiceType() string {
	return igd.srv.ServiceType
}

func IGDClientsByURL(ctx context.Context, url string) ([]IGDClient, error) {
	rd, err := DeviceByURL(ctx, url)
	if err != nil {
		return nil, err
	}

	var clients []IGDClient
	var visit func(Device)
	visit = func(d Device) {
		for _, srv := range d.Services {
			switch srv.ServiceType {
			case "urn:schemas-upnp-org:service:WANPPPConnection:1",
				"urn:schemas-upnp-org:service:WANIPConnection:1",
				"urn:schemas-upnp-org:service:WANIPConnection:2":
				clients = append(clients, IGDClient{urlBase: rd.URLBase, srv: srv})
			}
		}
		for _, d := range d.Devices {
			visit(d)
		}
	}
	visit(rd.Device)
	return clients, nil
}

// WithContext binds subsequent requests to ctx.
func (igd IGDClient) WithContext(ctx context.Context) IGDClient { igd.ctx = ctx; return igd }
