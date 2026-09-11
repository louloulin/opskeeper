package fleet

import (
	"context"
	"time"

	devicebiz "github.com/vincent-wuhan/opskeeper/internal/manager/biz/device"
	edgebiz "github.com/vincent-wuhan/opskeeper/internal/manager/biz/edge"
	devicemodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/device"
	edgemodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/edge"
)

type Filter struct {
	Status string
	Role   string
	Since  *time.Time
	Limit  int
	Offset int
}

type Host struct {
	Device *devicemodel.Device
	Edges  []*edgemodel.Edge
}

type DeviceRepo interface {
	List(context.Context, devicebiz.ListFilter) ([]*devicemodel.Device, error)
}

type EdgeRepo interface {
	List(context.Context, edgebiz.ListFilter) ([]*edgemodel.Edge, error)
}

type EdgeDeviceRepo interface {
	ListEdgesForDevice(context.Context, uint64) ([]*devicemodel.EdgeDevice, error)
}
