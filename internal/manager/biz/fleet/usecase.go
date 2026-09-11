package fleet

import (
	"context"
	"fmt"
	"strings"

	devicebiz "github.com/vincent-wuhan/opskeeper/internal/manager/biz/device"
	edgebiz "github.com/vincent-wuhan/opskeeper/internal/manager/biz/edge"
	devicemodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/device"
	edgemodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/edge"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/errs"
)

// Usecase assembles the fleet read model from the existing device, edge and
// edge_devices stores. No fleet-specific persistence is needed for F-1.
type Usecase struct {
	devices DeviceRepo
	edges   EdgeRepo
	links   EdgeDeviceRepo
}

func NewUsecase(devices DeviceRepo, edges EdgeRepo, links EdgeDeviceRepo) *Usecase {
	return &Usecase{devices: devices, edges: edges, links: links}
}

func (u *Usecase) List(ctx context.Context, f Filter) ([]Host, int, error) {
	if u.devices == nil || u.edges == nil || u.links == nil {
		return nil, 0, errs.ErrNotWiredYet
	}
	if f.Status != "" && f.Status != edgemodel.StatusOnline && f.Status != edgemodel.StatusOffline {
		return nil, 0, fmt.Errorf("%w: invalid fleet status %q", errs.ErrInvalid, f.Status)
	}
	role := strings.TrimSpace(f.Role)
	if role != "" && !devicemodel.IsValidRoleName(role) {
		return nil, 0, fmt.Errorf("%w: invalid fleet role %q", errs.ErrInvalid, role)
	}
	filter := devicebiz.ListFilter{}
	if f.Status != "" {
		online := f.Status == edgemodel.StatusOnline
		filter.Online = &online
	}
	if role != "" {
		if role == devicemodel.RoleUnknown {
			filter.RolesUnknownOnly = true
		} else {
			filter.RolesAny = devicemodel.EncodeRoles([]string{role})
		}
	}
	devices, err := u.devices.List(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	edges, err := u.edges.List(ctx, edgebiz.ListFilter{})
	if err != nil {
		return nil, 0, err
	}
	edgesByID := make(map[uint64]*edgemodel.Edge, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgesByID[edge.ID] = edge
		}
	}
	all := make([]Host, 0, len(devices))
	for _, device := range devices {
		if device == nil || (f.Since != nil && (device.LastSeenAt == nil || device.LastSeenAt.Before(*f.Since))) {
			continue
		}
		rows, err := u.links.ListEdgesForDevice(ctx, device.ID)
		if err != nil {
			return nil, 0, err
		}
		joined := make([]*edgemodel.Edge, 0, len(rows))
		for _, row := range rows {
			if edge, ok := edgesByID[row.EdgeID]; ok {
				joined = append(joined, edge)
			}
		}
		all = append(all, Host{Device: device, Edges: joined})
	}
	total := len(all)
	start := max(0, f.Offset)
	if start > total {
		start = total
	}
	end := total
	if f.Limit > 0 && start+f.Limit < end {
		end = start + f.Limit
	}
	return all[start:end], total, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
