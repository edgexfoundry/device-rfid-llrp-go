# EdgeX RFID LLRP Device Service Snap
[![snap store badge](https://raw.githubusercontent.com/snapcore/snap-store-badges/master/EN/%5BEN%5D-snap-store-black-uneditable.png)](https://snapcraft.io/edgex-device-rfid-llrp)

This folder contains snap packaging for the EdgeX RFID LLRP Device Service Snap

The snap currently supports both `amd64` and `arm64` platforms.

## Installation

### Installing snapd
The snap can be installed on any system that supports snaps. You can see how to install 
snaps on your system [here](https://snapcraft.io/docs/installing-snapd/6735).

However for full security confinement, the snap should be installed on an 
Ubuntu 18.04 LTS or later (Desktop or Server), or a system running Ubuntu Core 18 or later.

### Installing EdgeX Device RFID LLRP as a snap
The snap is published in the snap store at https://snapcraft.io/edgex-device-rfid-llrp.
You can see the current revisions available for your machine's architecture by running the command:

```bash
$ snap info edgex-device-rfid-llrp
```

The latest stable version of the snap can be installed using:

```bash
$ sudo snap install edgex-device-rfid-llrp
```

The latest development version of the snap can be installed using:

```bash
$ sudo snap install edgex-device-rfid-llrp --edge
```

**Note** - the snap has only been tested on Ubuntu Core, Desktop, and Server.

## Snap configuration

Device services implement a service dependency check on startup which ensures that all of the runtime dependencies of a particular service are met before the service transitions to active state.

Snapd doesn't support orchestration between services in different snaps. It is therefore possible on a reboot for a device service to come up faster than all of the required services running in the main edgexfoundry snap. If this happens, it's possible that the device service repeatedly fails startup, and if it exceeds the systemd default limits, then it might be left in a failed state. This situation might be more likely on constrained hardware (e.g. RPi).

This snap therefore implements a basic retry loop with a maximum duration and sleep interval. If the dependent services are not available, the service sleeps for the defined interval (default: 1s) and then tries again up to a maximum duration (default: 60s). These values can be overridden with the following commands:
    
To change the maximum duration, use the following command:

```bash
$ sudo snap set edgex-device-rfid-llrp startup-duration=60
```

To change the interval between retries, use the following command:

```bash
$ sudo snap set edgex-device-rfid-llrp startup-interval=1
```

The service can then be started as follows. The "--enable" option
ensures that as well as starting the service now, it will be automatically started on boot:

```bash
$ sudo snap start --enable edgex-device-rfid-llrp.device-rfid-llrp
```

## Subnet setup

The `DiscoverySubnets` setting needs to be provided before a device discovery can occur. This can be done in a number of ways:

- Using `snap set` to set your local subnet information. Example:

    ```bash
    $ sudo snap set edgex-device-rfid-llrp env.app-custom.discovery-subnets="192.168.10.0/24"
    
    $ curl -X POST http://localhost:59989/api/v2/discovery
    ```

    **NOTE:** This will only work after [this issue](https://github.com/edgexfoundry/app-functions-sdk-go/issues/1043) is resolved.

- Using a [content interface](#using-a-content-interface-to-set-device-configuration) to set device configuration


- Using the `auto-configure` command. 
    
    This command finds all local network interfaces which are online and non-virtual and sets the value of `DiscoverySubnets` 
in Consul. When running with security enabled, it requires a Consul token, so it needs to be run as follows:

    ```bash
    # get Consul ACL token
    CONSUL_TOKEN=$(sudo cat /var/snap/edgexfoundry/current/secrets/consul-acl-token/bootstrap_token.json | jq ".SecretID" | tr -d '"') 
    echo $CONSUL_TOKEN 

    # start the device service and connect the interfaces required for network interface discovery
    sudo snap start edgex-device-rfid-llrp.device-rfid-llrp 
    sudo snap connect edgex-device-rfid-llrp:network-control 
    sudo snap connect edgex-device-rfid-llrp:network-observe 

    # run the nework interface discovery, providing the Consul token
    edgex-device-rfid-llrp.auto-configure $CONSUL_TOKEN
    ```


### Using a content interface to set device configuration

The `device-config` content interface allows another snap to seed this snap with configuration directories under `$SNAP_DATA/config/device-rfid-llrp`.

Note that the `device-config` content interface does NOT support seeding of the Secret Store Token because that file is expected at a different path.

Please refer to [edgex-config-provider](https://github.com/canonical/edgex-config-provider), for an example and further instructions.


### Autostart
By default, the edgex-device-rfid-llrp disables its service on install, as the expectation is that the default profile configuration files will be customized, and thus this behavior allows the profile `configuration.toml` files in $SNAP_DATA to be modified before the service is first started.

This behavior can be overridden by setting the `autostart` configuration setting to "true". This is useful when configuration and/or device profiles are being provided via configuration or gadget snap content interface.

**Note** - this option is typically set from a gadget snap.

### Rich Configuration
While it's possible on Ubuntu Core to provide additional profiles via gadget 
snap content interface, quite often only minor changes to existing profiles are required. 

These changes can be accomplished via support for EdgeX environment variable 
configuration overrides via the snap's configure hook.
If the service has already been started, setting one of these overrides currently requires the
service to be restarted via the command-line or snapd's REST API. 
If the overrides are provided via the snap configuration defaults capability of a gadget snap, 
the overrides will be picked up when the services are first started.

The following syntax is used to specify service-specific configuration overrides:

```
env.<stanza>.<config option>
```
For instance, to setup an override of the service's Port use:
```
$ sudo snap set edgex-device-rfid-llrp env.service.port=2112
```
And restart the service:
```
$ sudo snap restart edgex-device-rfid-llrp.device-rfid-llrp
```

**Note** - at this time changes to configuration values in the [Writable] section are not supported.
For details on the mapping of configuration options to Config options, please refer to "Service Environment Configuration Overrides".

## Service Environment Configuration Overrides
**Note** - all of the configuration options below must be specified with the prefix: 'env.'

```
[Service]
service.health-check-interval           // Service.HealthCheckInterval
service.host                            // Service.Host
service.server-bind-addr                // Service.ServerBindAddr
service.port                            // Service.Port
service.max-result-count                // Service.MaxResultCount
service.max-request-size                // Service.MaxRequestSize
service.startup-msg                     // Service.StartupMsg
service.request-timeout                 // Service.RequestTimeout

[SecretStore]
secret-store.secrets-file               // SecretStore.SecretsFile
secret-store.disable-scrub-secrets-file // SecretStore.DisableScrubSecretsFile

[Clients.core-data]
clients.core-data.port                  // Clients.core-data.Port

[Clients.core-metadata]
clients.core-metadata.port              // Clients.core-metadata.Port

[Device]
device.update-last-connected            // Device.UpdateLastConnected
device.use-message-bus                  // Device.UseMessageBus

[AppCustom]
app-custom.discovery-subnets            // AppCustom.DiscoverySubnets
app-custom.probe-async-limit            // AppCustom.ProbeAsyncLimit
app-custom.probe-timeout-seconds        // AppCustom.ProbeTimeoutSeconds
app-custom.scan-port                    // AppCustom.ScanPort
app-custom.max-discover-duration-seconds // AppCustom.MaxDiscoverDurationSeconds
```
