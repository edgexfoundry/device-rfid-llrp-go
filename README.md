# device-llrp-go
Edgex device service to handle LLRP Messages from 3rd Party RFID Readers.

## Build and deploy ##

#### Prerequisites ####

 - Docker
 - GNU Make
 - Docker-compose
 
#### Build ####

```
git clone https://github.impcloud.net/RSP-Inventory-Suite/device-llrp-go.git
cd device-llrp-go
make build
```

#### Create Docker image ####
```
make docker
```

#### Run as docker-compose with other Edgex services (Geneva Release) ####
```
docker-compose -f docker-compose-geneva-redis-no-secty.yml up
```

#### Stop all the docker services ####
```
docker-compose down
```
