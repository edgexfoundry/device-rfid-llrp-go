# Device LLRP Go
[![license](https://img.shields.io/badge/license-Apache%20v2.0-blue.svg)](LICENSE)
## Overview
LLRP Micro Service - device service for connecting LLRP based devices to EdgeX.

## Installation and Execution ##

#### Prerequisites ####

 - Go language
 - GNU Make
 - Docker
 - Docker-compose
 
#### Build ####
```
make build
```

#### Build Docker image ####
```
sudo make docker
```

#### Docker-compose run with other Edgex services (Geneva Release) ####
```
sudo make run
```

#### Docker-compose stop ####
```
sudo make stop
```

### Device Discovery
Upon startup this service will probe the local network in an effort to discover devices that support LLRP.

This discovery also happens at a regular interval and can be configured via [EdgeX Consul](http://localhost:8500/ui/dc1/kv/edgex/devices/1.0/edgex-device-llrp/Device/Discovery/) for existing installations, and [configuration.toml](cmd/res/configuration.toml) for default values.

The local network is probed for every IP on the default LLRP port (`5084`). If a device returns LLRP response messages, a new EdgeX device is generated under the name format `IP_Port`, like so: `192.168.1.101_5084`.  
