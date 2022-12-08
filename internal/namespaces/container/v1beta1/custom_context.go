package container

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"github.com/scaleway/scaleway-cli/v2/internal/core"
	"github.com/scaleway/scaleway-cli/v2/internal/interactive"
	"github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

func containerContext() *core.Command {
	return &core.Command{
		Short:     `Context management commands`,
		Long:      `Context management commands.`,
		Namespace: "container",
		Resource:  "context",
	}
}

func containerContextTags(name string) []string {
	return []string{
		"scw-docker-context:b-a-a-s",
		"scw-docker-context:" + name,
	}
}

type createContextRequest struct {
	Name string   `json:"-"`
	Zone scw.Zone `json:"-"`
	Size uint64   `json:"-"`
}

const containerContextCreateDefaultSize = 50

func containerContextCreate() *core.Command {
	return &core.Command{
		Short:     `Create context storage`,
		Long:      `Create block storage that one can attach to a container context.`,
		Namespace: "container",
		Resource:  "context",
		Verb:      "create",
		// Deprecated:    false,
		ArgsType: reflect.TypeOf(createContextRequest{}),
		ArgSpecs: core.ArgSpecs{
			{
				Name:       "name",
				Required:   true,
				Deprecated: false,
				Positional: true,
			},
			{
				Name: "size",
				// TODO: shave off oversize storage with: docker builder prune --keep-storage 20000000000 -f
				Short: "Size of block storage in GB",
				Default: func(ctx context.Context) (value string, doc string) {
					d := fmt.Sprintf("%d", containerContextCreateDefaultSize)
					return d, d
				},
			},
			{
				Name: "ttl",
				// TODO: docker builder prune -f --filter='unused-for=1h
				Short: "Delete data older than this on next use of block storage",
				ValidateFunc: func(argSpec *core.ArgSpec, value interface{}) error {
					return fmt.Errorf("unimplemented!")
				},
			},
			core.ZoneArgSpec(),
		},
		Run: func(ctx context.Context, args interface{}) (i interface{}, e error) {
			request := args.(*createContextRequest)

			client := core.ExtractClient(ctx)
			api := instance.NewAPI(client)

			x := instance.VolumeVolumeTypeBSSD
			volumesResponse, err := api.ListVolumes(&instance.ListVolumesRequest{
				Zone:       request.Zone,
				VolumeType: &x,
				Tags:       containerContextTags(request.Name),
				Name:       scw.StringPtr(request.Name),
			})
			if err != nil {
				return nil, err
			}
			if volumesResponse.TotalCount != 0 {
				return nil, fmt.Errorf("A volume named %q in zone %q already exists!", request.Name, request.Zone)
			}

			return api.CreateVolume(&instance.CreateVolumeRequest{
				Zone:       request.Zone,
				Name:       request.Name,
				Tags:       containerContextTags(request.Name),
				VolumeType: instance.VolumeVolumeTypeBSSD,
				Size:       scw.SizePtr(scw.Size(request.Size) * scw.GB),
			})
		},
	}
}

type startContextRequest struct {
	Name                     string `json:"-"`
	Type                     string `json:"-"`
	AutoWriteToSSHKnownHosts bool   `json:"auto-write-to-ssh-known-hosts"`
}

func containerContextStart() *core.Command {
	const autowrite = "auto-write-to-ssh-known-hosts"
	return &core.Command{
		Short:     `Start a context machine`,
		Long:      `Start an instance with named block storage attached.`,
		Namespace: "container",
		Resource:  "context",
		Verb:      "start",
		// Deprecated:    false,
		ArgsType: reflect.TypeOf(startContextRequest{}),
		ArgSpecs: core.ArgSpecs{
			{
				Name:       "name",
				Required:   true,
				Deprecated: false,
				Positional: true,
			},
			{
				Name:     "type",
				Short:    "Machine type to use as Docker context. If none is passed will use DEV1-S",
				Default:  core.DefaultValueSetter("DEV1-S"),
				Required: true,
				EnumValues: []string{
					"GP1-XS",
					"GP1-S",
					"GP1-M",
					"GP1-L",
					"GP1-XL",
					"DEV1-S",
					"DEV1-M",
					"DEV1-L",
					"DEV1-XL",
					"RENDER-S",
					"STARDUST1-S",
					"ENT1-S",
					"ENT1-M",
					"ENT1-L",
					"ENT1-XL",
					"ENT1-2XL",
					"PRO2-XXS",
					"PRO2-XS",
					"PRO2-S",
					"PRO2-M",
					"PRO2-L",
					"PLAY2-PICO",
					"PLAY2-NANO",
					"PLAY2-MICRO",
					"GPU-3070-S",
				},
				ValidateFunc: func(argSpec *core.ArgSpec, value interface{}) error {
					// Allow all commercial types
					return nil
				},
			},
			{
				Name:    autowrite,
				Default: core.DefaultValueSetter("false"),
			},
		},
		Run: func(ctx context.Context, args interface{}) (i interface{}, e error) {
			if _, err := exec.LookPath("docker"); err != nil {
				return nil, fmt.Errorf("This requires the `docker` command to be installed.")
			}
			request := args.(*startContextRequest)

			client := core.ExtractClient(ctx)
			api := instance.NewAPI(client)

			x := instance.VolumeVolumeTypeBSSD
			volumesResponse, err := api.ListVolumes(&instance.ListVolumesRequest{
				VolumeType: &x,
				Tags:       containerContextTags(request.Name),
				Name:       scw.StringPtr(request.Name),
			}, scw.WithZones(scw.AllZones...))
			if err != nil {
				return nil, err
			}
			if volumesResponse.TotalCount != 1 {
				// Create some block storage on the fly if none exist yet
				defaultZone, ok := client.GetDefaultZone()
				if !ok {
					defaultZone = "fr-par-2"
				}
				if _, err := containerContextCreate().Run(ctx, &createContextRequest{
					Name: request.Name,
					Zone: defaultZone,
					Size: containerContextCreateDefaultSize,
				}); err != nil {
					return nil, err
				}
				volumesResponse, err = api.ListVolumes(&instance.ListVolumesRequest{
					Zone:       defaultZone,
					VolumeType: &x,
					Tags:       containerContextTags(request.Name),
					Name:       scw.StringPtr(request.Name),
				})
				if err != nil {
					return nil, err
				}
				if volumesResponse.TotalCount != 1 {
					return nil, fmt.Errorf("Could not find volume named %q", request.Name)
				}
			}

			ipsResponse, err := api.CreateIP(&instance.CreateIPRequest{
				Zone: volumesResponse.Volumes[0].Zone,
				Tags: containerContextTags(request.Name),
			})
			if err != nil {
				return nil, err
			}
			serverResponse, err := api.CreateServer(&instance.CreateServerRequest{
				Zone:           volumesResponse.Volumes[0].Zone,
				Tags:           containerContextTags(request.Name),
				Name:           "", // auto-generated
				CommercialType: request.Type,
				Image:          "docker",
				Volumes: map[string]*instance.VolumeServerTemplate{
					"1": {
						Boot: false,
						ID:   volumesResponse.Volumes[0].ID,
						Name: request.Name,
					},
				},
				PublicIP: scw.StringPtr(ipsResponse.IP.ID),
			})
			if err != nil {
				return nil, err
			}

			cloudInit := fmt.Sprintf(`
#cloud-config
device_aliases:
  cache_dev: /dev/disk/by-id/scsi-0SCW_b_ssd_volume-%s
disk_setup:
  cache_dev:
    table_type: gpt
fs_setup:
  - label: cache_fs
    device: cache_dev
    filesystem: ext4
mounts:
  - [ "cache_dev", "/var/lib/docker" ]
`[1:], volumesResponse.Volumes[0].ID)

			if err := api.SetAllServerUserData(&instance.SetAllServerUserDataRequest{
				Zone:     serverResponse.Server.Zone,
				ServerID: serverResponse.Server.ID,
				UserData: map[string]io.Reader{
					"cloud-init": strings.NewReader(cloudInit),
				},
			}); err != nil {
				return nil, err
			}

			if err := api.ServerActionAndWait(&instance.ServerActionAndWaitRequest{
				ServerID: serverResponse.Server.ID,
				Zone:     serverResponse.Server.Zone,
				Action:   instance.ServerActionPoweron,
			}); err != nil {
				return nil, err
			}

			serverIP := serverResponse.Server.PublicIP.Address.String()
			failedToConnect := true
			for range make([]struct{}, 600) {
				if conn, _ := net.DialTimeout("tcp", serverIP+":22", 2*time.Second); conn != nil {
					if err = conn.Close(); err != nil {
						return nil, err
					}
					failedToConnect = false
					break
				}
				time.Sleep(1 * time.Second)
			}
			if failedToConnect {
				return nil, fmt.Errorf("Could not reach instance over SSH!")
			}

			var ok bool
			if !request.AutoWriteToSSHKnownHosts {
				if ok, err = interactive.PromptBoolWithConfig(&interactive.PromptBoolConfig{
					Prompt: fmt.Sprintf(
						"Do you want to cleanup already known SSH fingerprints for %s and update with the new one?\n"+
							"You can add the flag %s=true to skip this step.\n",
						serverIP, autowrite),
					DefaultValue: false,
					Ctx:          ctx,
				}); err != nil {
					return nil, err
				}
			}
			if request.AutoWriteToSSHKnownHosts || ok {
				line := fmt.Sprintf("ssh-keyscan -H %q >>~/.ssh/known_hosts", serverIP)
				cmd := exec.CommandContext(ctx, "/bin/sh", "-c", line)
				cmd.Stdout = io.Discard
				if err := cmd.Run(); err != nil {
					return nil, err
				}
			}

			for _, line := range [][]string{
				{`docker`, `context`, `create`, `--docker`, `host=ssh://root@` + serverIP, request.Name},
				{`docker`, `context`, `use`, request.Name},
			} {
				cmd := exec.CommandContext(ctx, line[0], line[1:]...)
				cmd.Stdout = io.Discard
				if err := cmd.Run(); err != nil {
					return nil, err
				}
			}

			msg := fmt.Sprintf("Docker context %q created!", request.Name)
			return msg, nil
		},
	}
}

type stopContextRequest struct {
	Name string `json:"-"`
}

func containerContextStop() *core.Command {
	return &core.Command{
		Short:     `Stop a context machine`,
		Long:      `Stop a context and shutdown its compute resources.`,
		Namespace: "container",
		Resource:  "context",
		Verb:      "stop",
		// Deprecated:    false,
		ArgsType: reflect.TypeOf(stopContextRequest{}),
		ArgSpecs: core.ArgSpecs{
			{
				Name:       "name",
				Required:   true,
				Deprecated: false,
				Positional: true,
			},
		},
		Run: func(ctx context.Context, args interface{}) (i interface{}, e error) {
			request := args.(*stopContextRequest)

			client := core.ExtractClient(ctx)
			api := instance.NewAPI(client)

			serversResponse, err := api.ListServers(&instance.ListServersRequest{
				Tags: containerContextTags(request.Name),
			}, scw.WithZones(scw.AllZones...))
			if err != nil {
				return nil, err
			}
			if serversResponse.TotalCount != 1 {
				return nil, fmt.Errorf("Could not find context named %q", request.Name)
			}

			for _, line := range [][]string{
				{`docker`, `context`, `use`, `default`},
				{`docker`, `context`, `rm`, request.Name},
			} {
				cmd := exec.CommandContext(ctx, line[0], line[1:]...)
				cmd.Stdout = io.Discard
				cmd.Run() // Ignore errors
			}

			if bootVolume, ok := serversResponse.Servers[0].Volumes["0"]; ok {
				if _, err := api.DetachVolume(&instance.DetachVolumeRequest{
					Zone:     serversResponse.Servers[0].Zone,
					VolumeID: bootVolume.ID,
				}); err != nil {
					return nil, err
				}
			}

			serverActionResponse, err := api.ServerAction(&instance.ServerActionRequest{
				Zone:     serversResponse.Servers[0].Zone,
				ServerID: serversResponse.Servers[0].ID,
				Action:   instance.ServerActionTerminate,
			})
			if err != nil {
				return nil, err
			}

			if err := api.DeleteIP(&instance.DeleteIPRequest{
				Zone: serversResponse.Servers[0].Zone,
				IP:   serversResponse.Servers[0].PublicIP.Address.String(),
			}); err != nil {
				return nil, err
			}

			return serverActionResponse, nil
		},
	}
}

type deleteContextRequest struct {
	Name string `json:"-"`
}

func containerContextDelete() *core.Command {
	return &core.Command{
		Short:     `Remove context storage`,
		Long:      `Remove block storage that's used by a context.`,
		Namespace: "container",
		Resource:  "context",
		Verb:      "delete",
		// Deprecated:    false,
		ArgsType: reflect.TypeOf(deleteContextRequest{}),
		ArgSpecs: core.ArgSpecs{
			{
				Name:       "name",
				Required:   true,
				Deprecated: false,
				Positional: true,
			},
		},
		Run: func(ctx context.Context, args interface{}) (i interface{}, e error) {
			request := args.(*deleteContextRequest)

			client := core.ExtractClient(ctx)
			api := instance.NewAPI(client)

			x := instance.VolumeVolumeTypeBSSD
			response, err := api.ListVolumes(&instance.ListVolumesRequest{
				VolumeType: &x,
				Tags:       containerContextTags(request.Name),
				Name:       scw.StringPtr(request.Name),
			}, scw.WithZones(scw.AllZones...))
			if err != nil {
				return nil, err
			}
			if response.TotalCount != 1 {
				return nil, fmt.Errorf("Could not find volume named %q", request.Name)
			}

			if err = api.DeleteVolume(&instance.DeleteVolumeRequest{
				Zone:     response.Volumes[0].Zone,
				VolumeID: response.Volumes[0].ID,
			}); err != nil {
				return nil, err
			}

			msg := fmt.Sprintf("Volume named %q successfully deleted from zone %q",
				request.Name,
				response.Volumes[0].Zone,
			)
			return msg, nil
		},
	}
}
