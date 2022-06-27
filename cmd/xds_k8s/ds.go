package main

import (
	"errors"
	"fmt"
	"log"

	api "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	envoy_core "github.com/envoyproxy/go-control-plane/envoy/api/v2/core"
	ep "github.com/envoyproxy/go-control-plane/envoy/api/v2/endpoint"
	route "github.com/envoyproxy/go-control-plane/envoy/api/v2/route"
	hcm "github.com/envoyproxy/go-control-plane/envoy/config/filter/network/http_connection_manager/v2"
	listener "github.com/envoyproxy/go-control-plane/envoy/config/listener/v2"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	xds_cache "github.com/envoyproxy/go-control-plane/pkg/cache/v2"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v2"
	"github.com/golang/protobuf/ptypes"
	"google.golang.org/protobuf/types/known/wrapperspb"
	core "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
)

const (
	regionNameHC = "tdb_hardcoded"
	zoneNameHC   = "dataline_dc"

	serviceName = "backend"
)

type (
	podEndpoint struct {
		ip   string
		port int32
	}
)

func (c *k8sInMemoryState) initState(epsInformer cache.SharedIndexInformer) error {
	svc2Eps := make(map[string][]podEndpoint)

	for _, obj := range epsInformer.GetStore().List() {
		endpoint := obj.(*core.Endpoints)
		if endpoint == nil {
			return errors.New("endpoints object expected")
		}

		epSvcName := endpoint.GetObjectMeta().GetName()
		if _, ok := svc2Eps[epSvcName]; ok || epSvcName != serviceName {
			continue
		}

		svc2Eps[epSvcName] = extractDataFromEndpoints(endpoint)
	}

	c.svcState = svc2Eps

	eds, cds, rds, lds, err := getResources(svc2Eps, map[string]string{zoneNameHC: regionNameHC})
	if err != nil {
		panic(err)
	}

	log.Printf("setting new caches:\nEDS: %+v\nCDS: %+v\nRDS: %+v\nLDS: %+v\n\n\n", eds, cds, rds, lds)

	c.lcacheEds = xds_cache.NewLinearCache(resource.EndpointType, xds_cache.WithInitialResources(eds))
	c.lcacheCds = xds_cache.NewLinearCache(resource.ClusterType, xds_cache.WithInitialResources(cds))
	c.lcacheRds = xds_cache.NewLinearCache(resource.RouteType, xds_cache.WithInitialResources(rds))
	c.lcacheLds = xds_cache.NewLinearCache(resource.ListenerType, xds_cache.WithInitialResources(lds))

	epsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onAdd,
		UpdateFunc: c.onUpdate,
		DeleteFunc: c.onDelete,
	})

	c.muxCache = &xds_cache.MuxCache{
		// nolint: govet
		Classify: func(request xds_cache.Request) string {
			return request.TypeUrl
		},
		Caches: map[string]xds_cache.Cache{
			resource.EndpointType: c.lcacheEds,
			resource.ClusterType:  c.lcacheCds,
			resource.RouteType:    c.lcacheRds,
			resource.ListenerType: c.lcacheLds,
		},
	}

	return nil
}

func getResources(svc2Eps map[string][]podEndpoint, localitiesZone2Reg map[string]string) (eds, cds, rds, lds map[string]types.Resource, err error) {
	eds = map[string]types.Resource{}
	cds = map[string]types.Resource{}
	rds = map[string]types.Resource{}
	lds = map[string]types.Resource{}

	for svc, eps := range svc2Eps {
		eds[svc] = getEDS(svc, eps, localitiesZone2Reg)
		cds[svc] = getCDS(svc, svc)

		svcRouteConfigName := svc + "-route-config"

		rds[svcRouteConfigName] = getRDS(svc, svcRouteConfigName, svc+"-vh", svc)
		v, lerr := getLDS(svcRouteConfigName, svc)
		if lerr != nil {
			err = fmt.Errorf("failed getting lds for '%s': %w", svc, lerr)
			return
		}
		lds[svc] = v
	}

	return
}

func getLDS(svcRouteConfigName, svcListenerName string) (types.Resource, error) {
	httpConnRds := &hcm.HttpConnectionManager_Rds{
		Rds: &hcm.Rds{
			RouteConfigName: svcRouteConfigName,
			ConfigSource: &envoy_core.ConfigSource{
				ConfigSourceSpecifier: &envoy_core.ConfigSource_Ads{
					Ads: &envoy_core.AggregatedConfigSource{},
				},
			},
		},
	}

	httpConnManager := &hcm.HttpConnectionManager{
		CodecType:      hcm.HttpConnectionManager_AUTO,
		RouteSpecifier: httpConnRds,
	}

	anyListener, err := ptypes.MarshalAny(httpConnManager)
	if err != nil {
		return nil, fmt.Errorf("failed marshal any proto: %w", err)
	}

	return types.Resource(
		&api.Listener{
			Name: svcListenerName,
			ApiListener: &listener.ApiListener{
				ApiListener: anyListener,
			},
		}), nil
}

func getRDS(clusterName, svcRouteConfigName, svcVirtualHostName, svcListenerName string) types.Resource {
	vhost := &route.VirtualHost{
		Name:    svcVirtualHostName,
		Domains: []string{svcListenerName},
		Routes: []*route.Route{{
			Match: &route.RouteMatch{
				PathSpecifier: &route.RouteMatch_Prefix{Prefix: ""},
			},
			Action: &route.Route_Route{Route: &route.RouteAction{

				ClusterSpecifier: &route.RouteAction_WeightedClusters{
					WeightedClusters: &route.WeightedCluster{
						TotalWeight: &wrapperspb.UInt32Value{Value: uint32(100)}, // default value
						Clusters: []*route.WeightedCluster_ClusterWeight{
							{
								Name:   clusterName,
								Weight: &wrapperspb.UInt32Value{Value: uint32(100)},
							},
						},
					},
				}}},
		}},
	}

	return types.Resource(
		&api.RouteConfiguration{
			Name:         svcRouteConfigName,
			VirtualHosts: []*route.VirtualHost{vhost},
		})
}

func getCDS(clusterName, serviceName string) types.Resource {
	return types.Resource(
		&api.Cluster{
			Name:                 clusterName,
			LbPolicy:             api.Cluster_ROUND_ROBIN,                  // as of grpc-go 1.32.x it's the only option
			ClusterDiscoveryType: &api.Cluster_Type{Type: api.Cluster_EDS}, // points to EDS
			EdsClusterConfig: &api.Cluster_EdsClusterConfig{
				EdsConfig: &envoy_core.ConfigSource{
					ConfigSourceSpecifier: &envoy_core.ConfigSource_Ads{}, // as of grpc-go 1.32.x it's the only option for DS config source
				},
				ServiceName: serviceName,
			},
			CommonLbConfig: &api.Cluster_CommonLbConfig{
				LocalityConfigSpecifier: &api.Cluster_CommonLbConfig_LocalityWeightedLbConfig_{
					LocalityWeightedLbConfig: &api.Cluster_CommonLbConfig_LocalityWeightedLbConfig{},
				},
			},
		})
}

func getEDS(serviceName string, endpoints []podEndpoint, localitiesZone2Reg map[string]string) types.Resource {
	var (
		weights = []uint32{70, 20, 0}
		lbeps   []*ep.LbEndpoint
		sumW    uint32
	)

	for i, podEp := range endpoints {
		hst := &envoy_core.Address{
			Address: &envoy_core.Address_SocketAddress{
				SocketAddress: &envoy_core.SocketAddress{
					Address:  podEp.ip,
					Protocol: envoy_core.SocketAddress_TCP,
					PortSpecifier: &envoy_core.SocketAddress_PortValue{
						PortValue: uint32(podEp.port),
					},
				},
			},
		}

		w := weights[i%len(weights)]
		sumW += w

		lbeps = append(lbeps, &ep.LbEndpoint{
			HostIdentifier: &ep.LbEndpoint_Endpoint{
				Endpoint: &ep.Endpoint{
					Address: hst,
				},
			},
			HealthStatus: envoy_core.HealthStatus_HEALTHY,
			LoadBalancingWeight: &wrapperspb.UInt32Value{
				Value: w,
			},
		})
	}

	var localityLbEps []*ep.LocalityLbEndpoints
	for zone, region := range localitiesZone2Reg {
		localityLbEps = append(localityLbEps, &ep.LocalityLbEndpoints{
			Priority: 0,
			LoadBalancingWeight: &wrapperspb.UInt32Value{
				Value: sumW,
			},
			LbEndpoints: lbeps,
			Locality: &envoy_core.Locality{
				Zone:   zone,
				Region: region,
			},
		})
	}

	return types.Resource(
		&api.ClusterLoadAssignment{
			ClusterName: serviceName,
			Endpoints:   localityLbEps,
		})
}
