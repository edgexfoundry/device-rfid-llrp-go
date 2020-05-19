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

#### Docker-compose run with other Edgex services (Geneva Release) ####
```
sudo make run
```

#### Docker-compose stop ####
```
sudo make stop
```
## License
[Apache-2.0](LICENSE)
