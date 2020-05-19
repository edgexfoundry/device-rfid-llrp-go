# Device LLRP Go
## Overview
LLRP Micro Service - device service for connecting LLRP based devices to EdgeX.

## Installation and Execution ##

#### Prerequisites ####

 - GNU Make
 - Docker
 - Docker-compose
 
#### Build ####

```
sudo make build
```

#### Build Docker image ####
```
sudo make docker
```

#### Run as docker-compose with other Edgex services (Geneva Release) ####
```
sudo docker-compose -f docker-compose-geneva-redis-no-secty.yml up -d
```

#### Stop and remove the docker services ####
```
sudo docker-compose -f docker-compose-geneva-redis-no-secty.yml down
```
## License
[Apache-2.0](LICENSE)
