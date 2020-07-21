# Device LLRP Go
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
## License
[Apache-2.0](LICENSE)


## Example Scripts
There are a couple of example scripts here
to interact with devices through EdgeX's APIs.
They aren't meant to be perfect or necessarily the best way to do things,
but they should help give examples of what's possible.
They don't do much error handling, 
so don't rely on them for much more than happy-path testing. 

 - [command.sh](examples/command.sh):
    interacts with the commands service
    to get/set LLRP configs and such.
 - [data.sh](examples/data.sh):
    interacts with the data service to view reports and the like. 
 - [example.sh](examples/example.sh):
    runs a "full" example --
    sends/enables [`ROSpec.json`](examples/ROSpec.json) 
    to the first reader that EdgeX knows about,
    waits a bit, disables/deletes it from the reader,
    then displays any collected tags.
 
They assume everything is running and expect you have a these on your path:
`jq`, `curl`, `sed`, `xargs`, `base64`, and `od`. 
By default, they all try to connect to `localhost` on the typical EdgeX ports.
`commands.sh` and `data.sh` take args/options; use `--help` to see their usage.
`example.sh` uses a couple of variables defined at the top of the file
to determine which host/port/file to use.
