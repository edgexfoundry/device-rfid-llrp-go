#!/usr/bin/env bash

# This script configures the subnet information for device discovery.
# The DiscoverySubnets config option defaults to blank in the configuration.toml file, and needs to be provided before
# a discovery can occur. This script checks your local machine's network interfaces to see which
# ones are both online and a physical device. It uses this information to fill in the
# DiscoverySubnets field in Consul for you.

set -eu

spacing=18; prev_line="\e[1A\e[$((spacing + 2))C"
green="\e[32m"; red="\e[31m"; clear="\e[0m"; bold="\e[1m"; normal="\e[22;24m"

trap 'err' ERR
err() {
    echo -e "${red}${bold}Failed!${clear}"
    exit 1
}

CONSUL_URL=${CONSUL_URL:-http://localhost:8500}
url="${CONSUL_URL}/v1/kv/edgex/devices/1.0/edgex-device-rfid-llrp/Driver/DiscoverySubnets"

### Dependencies Check
printf "${bold}%${spacing}s${clear}: ...\n${red}" "Dependencies Check"
if ! type -P curl >/dev/null; then
    echo -e "${bold}${red}Failed!${normal} Please install ${bold}curl${normal} in order to use this script!${clear}"
    exit 1
fi
echo -e "${prev_line}${green}Success${clear}"

### Consul Check
printf "${bold}%${spacing}s${clear}: ...\n${red}" "Consul Check"
code=$(curl -X GET -w "%{http_code}" -o /dev/null -s "${url}" || echo $?)
if [ $((code)) -ne 200 ]; then
    echo -e "${red}${bold}Failed!${normal} curl returned a status code of '${bold}${code}'${normal}"
    if [ $((code)) -eq 404 ]; then
        echo -e "* Have you deployed the ${bold}device-rfid-llrp${normal} service?${clear}"
    else
        echo -e "* Is Consul deployed and accessible?${clear}"
    fi
    exit $((code))
fi
echo -e "${prev_line}${green}Success${clear}"

### Detect Interfaces
printf "${bold}%${spacing}s${clear}: ...\n${red}" "Interfaces"
# find all online non-virtual network interfaces, separated by `|` for regex matching. ie. (eno1|eno2|eno3|...)
ifaces=$(
    find /sys/class/net -mindepth 1 -maxdepth 2 \
        -not -lname '*devices/virtual*' \
        -execdir grep -q 'up' "{}/operstate" \; \
        -printf '%f|'
)
if [ -z "${ifaces}" ]; then
    echo "Error, no online physical network interfaces detected.${clear}"
    exit 1
fi
echo -e "${prev_line}${clear}${ifaces//|/ }"

### Detect Subnets
printf "${bold}%${spacing}s${clear}: ...\n${red}" "Subnets"
# print all ipv4 subnets, filter for just the ones associated with our physical interfaces
# grab the unique ones and join them by commas
subnets=$(
    ip -4 -o route list scope link |
        sed -En "s/ dev (${ifaces::-1}).+//p" |
        grep -v "169.254.0.0/16" | # Remove link-local subnet
        sort -u |
        paste -sd, -
)

if [ -z "${subnets}" ]; then
    echo -e "Error, no subnets detected.${clear}"
    exit 1
fi
echo -e "${prev_line}${clear}${subnets}"

### Configure Consul
printf "${bold}%${spacing}s${clear}: ...\n${red}" "Configure"
code=$(curl -X PUT --data "${subnets}" -w "%{http_code}" -o /dev/null -s "${url}" || echo $?)
if [ $((code)) -ne 200 ]; then
    echo -e "${red}${bold}Failed!${normal} curl returned a status code of '${bold}${code}'${clear}"
    exit $((code))
fi
echo -e "${prev_line}${green}Success${clear}"
