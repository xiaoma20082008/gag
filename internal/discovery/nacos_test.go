package discovery

import (
	"context"
	"errors"
	"gag/internal/config"
	"testing"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// fakeNamingClient 只关心 SelectInstances：其余方法走 INamingClient 嵌入的
// nil 接口，被调用即 panic——测试里本就不该触发。
type fakeNamingClient struct {
	naming_client.INamingClient

	param  vo.SelectInstancesParam
	result []model.Instance
	err    error
}

func (f *fakeNamingClient) SelectInstances(param vo.SelectInstancesParam) ([]model.Instance, error) {
	f.param = param
	return f.result, f.err
}

func clientWith(fake *fakeNamingClient) *NacosDiscoveryClient {
	return &NacosDiscoveryClient{sdk: fake, cfg: &config.DiscoveryClientConfig{}}
}

func TestDiscoverInstancesMapsRegistryData(t *testing.T) {
	fake := &fakeNamingClient{result: []model.Instance{{
		InstanceId: "10.0.0.1#9000#DEFAULT#order-hz",
		Ip:         "10.0.0.1",
		Port:       9000,
		Weight:     2,
		Metadata:   map[string]string{"version": "v2"},
	}}}

	insts, err := clientWith(fake).DiscoverInstances(context.Background(), "t1", "order", "order-hz")
	if err != nil {
		t.Fatalf("DiscoverInstances 失败: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("实例数量 = %d, want 1", len(insts))
	}

	in := insts[0]
	if in.ID != "10.0.0.1#9000#DEFAULT#order-hz" {
		t.Errorf("ID = %q", in.ID)
	}
	if in.Host != "10.0.0.1" || in.Port != 9000 || in.Address != "10.0.0.1:9000" {
		t.Errorf("Host/Port/Address = %q/%d/%q", in.Host, in.Port, in.Address)
	}
	if in.Weight != 2 {
		t.Errorf("Weight = %d, want 2", in.Weight)
	}
	if in.Metadata["version"] != "v2" {
		t.Errorf("Metadata = %v", in.Metadata)
	}

	// 租户映射到分组、服务名原样、集群名传给 SDK 的集群过滤器；
	// HealthyOnly 必须为 true，否则 SDK 返回的是不健康的实例。
	if fake.param.GroupName != "t1" || fake.param.ServiceName != "order" {
		t.Errorf("SDK 定位参数 = %+v", fake.param)
	}
	if len(fake.param.Clusters) != 1 || fake.param.Clusters[0] != "order-hz" {
		t.Errorf("SDK 集群过滤 = %v", fake.param.Clusters)
	}
	if !fake.param.HealthyOnly {
		t.Error("HealthyOnly 必须为 true，否则拿到的是不健康实例")
	}
}

func TestDiscoverInstancesEmptyListErrorIsNotFailure(t *testing.T) {
	// SDK 对"列表为空"返回空切片+error，查询失败返回 nil+error；
	// 前者要让调用期按"没有实例"处理，不能升级成基础设施故障。
	fake := &fakeNamingClient{result: []model.Instance{}, err: errors.New("instance list is empty!")}

	insts, err := clientWith(fake).DiscoverInstances(context.Background(), "t1", "order", "order-hz")
	if err != nil {
		t.Fatalf("空实例列表应被吞掉: %v", err)
	}
	if len(insts) != 0 {
		t.Fatalf("实例数量 = %d, want 0", len(insts))
	}
}

func TestDiscoverInstancesQueryFailurePropagates(t *testing.T) {
	boom := errors.New("connect: connection refused")
	fake := &fakeNamingClient{err: boom}

	if _, err := clientWith(fake).DiscoverInstances(context.Background(), "t1", "order", "order-hz"); !errors.Is(err, boom) {
		t.Fatalf("注册中心查询失败必须原样透传, got %v", err)
	}
}

func TestWarmQueriesOnce(t *testing.T) {
	fake := &fakeNamingClient{result: []model.Instance{{Ip: "10.0.0.2", Port: 9000, Weight: 1}}}

	if err := clientWith(fake).Warm(context.Background(), "t1", "order", "order-hz"); err != nil {
		t.Fatalf("Warm 失败: %v", err)
	}
	if fake.param.ServiceName != "order" {
		t.Errorf("Warm 没有真正查询: %+v", fake.param)
	}
}

func TestNewNacosDiscoveryClientValidatesConfig(t *testing.T) {
	if _, err := NewNacosDiscoveryClient(nil); err == nil {
		t.Error("nil 配置应报错")
	}
	if _, err := NewNacosDiscoveryClient(&config.DiscoveryClientConfig{Host: ""}); err == nil {
		t.Error("缺少 host 应报错")
	}
}

func TestMapToInstancesEmpty(t *testing.T) {
	if got := clientWith(&fakeNamingClient{}).mapToInstances(nil); len(got) != 0 {
		t.Fatalf("nil 输入应映射为空切片, got %v", got)
	}
}

// 编译期确认：客户端可以注入任何 INamingClient 实现，测试才能不依赖真实注册中心。
var _ naming_client.INamingClient = (*fakeNamingClient)(nil)
