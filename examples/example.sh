#!/usr/bin/env bash
# example.sh - sends an ROSpec, enables it,
#   waits a bit, prints tag reads, disables/deletes the RO.
#
# This script is very simple and has basically no error checking.
# Use at your own risk.
#
# This script assumes you have the following tools available:
#   jq, sed, xargs, curl, base64, od
# And it assumes the service/edgex is already running correctly.
# The host & ports it will use are found in the variables below:

HOST=localhost
DATA_PORT=59880
META_PORT=59881
CMDS_PORT=59882
ROSPEC_LOCATION="ROSpec.json"

set -euo pipefail
IFS=$'\n\t'

# Get the first device
device=$(curl -so- ${HOST}:${META_PORT}/api/v2/device/all | jq '[.devices[]|select(.serviceName=="device-rfid-llrp" )]' | jq '.[0].name' | tr -d '"')
dev_url=${HOST}:${CMDS_PORT}/api/v2/device/name/${device}

echo "Using ${dev_url}"


# Add an ROSpec
echo "Adding ROSpec ${ROSPEC_LOCATION}"
curl -so- "${dev_url}" | jq '.deviceCoreCommand.coreCommands[]|select(.name=="ROSpec")|.url+.path' | sed -e "s/edgex-core-command/${HOST}/" | xargs -L1 \
    curl -so- -X PUT -H 'Content-Type: application/json' --data '@'<(echo "{\"ROSpec\":$(<"$ROSPEC_LOCATION")}") 

# Get ROSpecs
echo "Getting ROSpecs from Reader"
curl -so- "${dev_url}" | jq '.deviceCoreCommand.coreCommands[]|select(.name=="ROSpec")|.url+.path' | sed -e "s/edgex-core-command/${HOST}/" | xargs -L1 \
     curl -so- | jq '.event.readings[].objectValue.ROSpecs'
​
# Enable ROSpec
echo "Enabling ROSpec 1"
curl -so- "${dev_url}" | jq '.deviceCoreCommand.coreCommands[]|select(.name=="enableROSpec")|.url+.path' | sed -e "s/edgex-core-command/${HOST}/" | xargs -L1 \
    curl -so- -X PUT -H 'Content-Type: application/json' --data '{"ROSpecID": "1"}'
​
# wait a bit
for i in {10..1}; do
    echo "Waiting ${i} more seconds..."
    sleep 1
done
​
​
# Disable ROSpec
echo "Disabling ROSpec 1"
curl -so- "${dev_url}" | jq '.deviceCoreCommand.coreCommands[]|select(.name=="disableROSpec")|.url+.path' | sed -e "s/edgex-core-command/${HOST}/" | xargs -L1 \
    curl -so- -X PUT -H 'Content-Type: application/json' --data '{"ROSpecID": "1"}'
​
# Delete ROSpec
echo "Deleting ROSpec 1"
curl -so- "${dev_url}" | jq '.deviceCoreCommand.coreCommands[]|select(.name=="deleteROSpec")|.url+.path' | sed -e "s/edgex-core-command/${HOST}/" | xargs -L1 \
    curl -so- -X PUT -H 'Content-Type: application/json' --data '{"ROSpecID": "1"}'
​
# See collected EPCs (assuming EPC96)
echo "Displaying EPCs"
curl -so- ${HOST}:${DATA_PORT}/api/v2/reading/all?limit=1000 | \
    jq '.readings[].objectValue.TagReportData[]?|.EPC96.EPC//.EPCData.EPC' | tr -d '"' | base64 -d | od --endian=big -t x2 -An -w12 -v | sort | uniq -c
