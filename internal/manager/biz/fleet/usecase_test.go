package fleet

import (
	"context"
	"errors"
	"testing"
	"time"

	devicebiz "github.com/vincent-wuhan/opskeeper/internal/manager/biz/device"
	edgebiz "github.com/vincent-wuhan/opskeeper/internal/manager/biz/edge"
	devicemodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/device"
	edgemodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/edge"
	"github.com/vincent-wuhan/opskeeper/internal/pkg/errs"
)

type fakeDevices struct {
	rows []*devicemodel.Device
	last devicebiz.ListFilter
}

func (f *fakeDevices) List(_ context.Context, filter devicebiz.ListFilter) ([]*devicemodel.Device, error) {
	f.last = filter
	return f.rows, nil
}

type fakeEdges struct{ rows []*edgemodel.Edge }

func (f *fakeEdges) List(context.Context, edgebiz.ListFilter) ([]*edgemodel.Edge, error) {
	return f.rows, nil
}

type fakeLinks struct {
	byDevice map[uint64][]*devicemodel.EdgeDevice
}

func (f *fakeLinks) ListEdgesForDevice(_ context.Context, id uint64) ([]*devicemodel.EdgeDevice, error) {
	return f.byDevice[id], nil
}

func TestUsecaseListJoinsHostEdgesAndPaginates(t *testing.T) {
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	devices := &fakeDevices{rows: []*devicemodel.Device{
		{ID: 2, Name: "db-b", LastSeenAt: &now, Roles: devicemodel.RoleBitDatabase, Online: true},
		{ID: 1, Name: "db-a", LastSeenAt: &now, Roles: devicemodel.RoleBitDatabase, Online: true},
	}}
	edges := &fakeEdges{rows: []*edgemodel.Edge{
		{ID: 10, Name: "edge-a", Status: edgemodel.StatusOnline},
		{ID: 11, Name: "edge-b", Status: edgemodel.StatusOffline},
	}}
	links := &fakeLinks{byDevice: map[uint64][]*devicemodel.EdgeDevice{
		2: {{EdgeID: 10, DeviceID: 2, Type: devicemodel.EdgeDeviceRelationHost}},
		1: {{EdgeID: 11, DeviceID: 1, Type: devicemodel.EdgeDeviceRelationHost}},
	}}

	got, total, err := NewUsecase(devices, edges, links).List(context.Background(), Filter{
		Status: edgemodel.StatusOnline, Role: devicemodel.RoleDatabase, Since: &now, Limit: 1, Offset: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(got) != 1 || got[0].Device.ID != 1 || len(got[0].Edges) != 1 || got[0].Edges[0].ID != 11 {
		t.Fatalf("got=%+v total=%d", got, total)
	}
	if devices.last.Online == nil || !*devices.last.Online || devices.last.RolesAny != devicemodel.RoleBitDatabase {
		t.Fatalf("device filter=%+v", devices.last)
	}
}

func TestUsecaseListRejectsUnknownFilterValues(t *testing.T) {
	u := NewUsecase(&fakeDevices{}, &fakeEdges{}, &fakeLinks{})
	for _, filter := range []Filter{{Status: "degraded"}, {Role: "cache"}} {
		if _, _, err := u.List(context.Background(), filter); !errors.Is(err, errs.ErrInvalid) {
			t.Errorf("filter=%+v error=%v, want ErrInvalid", filter, err)
		}
	}
}
