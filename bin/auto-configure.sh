#!/usr/bin/env bash

set -eu

CONSUL_URL=${CONSUL_URL:-http://localhost:8500}
url="${CONSUL_URL}/v1/kv/edgex/devices/1.0/edgex-device-rfid-llrp/Driver/DiscoverySubnets"

printf "\e[1m%18s\e[0m: ..." "Dependencies Check"
if ! type -P curl > /dev/null; then
    printf "\e[3D\e[1;31mFailed!\e[22;24m
Please install \e[1mcurl\e[22;24m in order to use this script!\e[0m
"
    exit 1
fi
printf "\e[3D\e[32mSuccess\e[0m\n"

printf "\e[1m%18s\e[0m: ..." "Consul Check"
code=$(curl -X GET -w "%{http_code}" -o /dev/null -s "${url}")

if [ "${code}" -ne 200 ]; then
    printf "\e[3D\e[1;31mFailed!\e[22;24m
curl returned a status code of \e[1m%s\e[0m
" "${code}" 1>&2
    if [ "${code}" -eq 404 ]; then
        printf -- "* Have you deployed the \e[1mdevice-rfid-llrp\e[0m service?\n"
    else
        printf -- "* Is Consul deployed and accessible?\n"
    fi
    exit $((code))
fi
printf "\e[3D\e[32mSuccess\e[0m\n"

# find all online non-virtual network interfaces, separated by `|` for regex matching. ie. (eno1|eno2|eno3|...)
ifaces=$(
    find /sys/class/net -mindepth 1 -maxdepth 2 \
        -not -lname '*devices/virtual*' \
        -execdir grep -q 'up' "{}/operstate" \; \
        -printf '%f|'
)

# print all ipv4 subnets, filter for just the ones associated with our physical interfaces
# grab the unique ones and join them by commas
subnets=$(
    ip -4 -o route list scope link | \
    sed -En "s/ dev (${ifaces::-1}).+//p" | \
    sort -u | \
    paste -sd, -
)

printf "\e[1m%18s\e[0m: %s\n" "Subnets" "${subnets}"
if [ -z "${subnets}" ]; then
    echo "Error, no subnets detected" 1>&2
    exit 1
fi

printf "\e[1m%18s\e[0m: ..." "Configure"
code=$(curl -X PUT --data "${subnets}" -w "%{http_code}" -o /dev/null -s "${url}")
if [ "${code}" -ne 200 ]; then
    printf "\e[3D\e[1;31mFailed!\e[31m
curl returned a status code of \e[1m%s\e[0m
" "${code}"
    exit $((code))
fi
printf "\e[3D\e[32mSuccess\e[0m\n"
