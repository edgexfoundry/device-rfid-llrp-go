#!/usr/bin/env bash

#
# Copyright (C) 2020-2021 Intel Corporation
#
# SPDX-License-Identifier: Apache-2.0
#

#
# The purpose of this script is to make it easier for an end user to configure LLRP device discovery
# without the need to have knowledge about subnets and/or CIDR format. The "DiscoverySubnets" config
# option defaults to blank in the configuration.toml file, and needs to be provided before a discovery can occur.
# This allows the LLRP device service to be run in a NAT-ed environment without host-mode networking because the subnet information
# is user-provided and does not rely on the device-rfid-llrp service to detect it.
#
# Essentially how this script works is it polls the machine it is running on and finds the active subnet for
# any and all network interfaces that are on the machine which are physical (non-virtual) and online.
# It uses this information to automatically fill out the "DiscoverySubnets" configuration option through Consul of a deployed
# device-rfid-llrp instance.
#
# NOTE 1: This script requires EdgeX Consul and the device-rfid-llrp service to have been run before this
# script will function.
#
# NOTE 2: If the "DiscoverySubnets" config is provided via "configuration.toml" this script does
# not need to be run.
#

set -eu

# If ran with DEBUG=1, enable bash command tracing
DEBUG=${DEBUG:-0}
if [ "${DEBUG}" == "1" ]; then
    set -x
fi

spacing=18; prev_line="\e[1A\e[$((spacing + 2))C"
green="\e[32m"; red="\e[31m"; clear="\e[0m"; bold="\e[1m"; normal="\e[22;24m"

trap 'err' ERR
err() {
    echo -e "${red}${bold}Failed!${clear}"
    exit 1
}

CONSUL_URL=${CONSUL_URL:-http://localhost:8500}
url="${CONSUL_URL}/v1/kv/edgex/devices/2.0/device-rfid-llrp/Driver/DiscoverySubnets"

### Dependencies Check
# Note: trailing ${red} is to colorize red all potential error output from the following commands
printf "${bold}%${spacing}s${clear}: ...\n${red}" "Dependencies Check"
if ! type -P curl >/dev/null; then
    echo -e "${bold}${red}Failed!${normal} Please install ${bold}curl${normal} in order to use this script!${clear}"
    exit 1
fi
echo -e "${prev_line}${green}Success${clear}"

### Consul Check
# Note: trailing ${red} is to colorize red all potential error output from the following commands
printf "${bold}%${spacing}s${clear}: ...\n${red}" "Consul Check"
code=$(curl -X GET -w "%{http_code}" -o /dev/null -s "${url}" || echo $?)
if [ $((code)) -ne 200 ]; then
    echo -e "${red}${bold}Failed!${normal} curl returned a status code of '${bold}$((code))'${normal}"
    # Special message for error code 7
    if [ $((code)) -eq 7 ]; then
      echo -e "* Error code '7' denotes 'Failed to connect to host or proxy'"
    fi
    # Error 404 means it connected to consul but couldn't find the key
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
# find all online non-virtual network interfaces and print them separated by `|` for regex matching.
# example output: eno1|eno2|eno3|...|
# note: a trailing '|' is produced which is removed in a later step
ifaces=$(
    find /sys/class/net -mindepth 1 -maxdepth 2   `# list all network interfaces`  \
        -not -lname '*devices/virtual*'           `# filter out all virtual interfaces` \
        -execdir grep -q 'up' "{}/operstate" \;   `# ensure interface is online (operstate == up)` \
        -printf '%f|'                             `# print them separated by | for regex matching`
)
if [ -z "${ifaces}" ]; then
    echo "Error, no online physical network interfaces detected.${clear}"
    exit 1
fi
echo -e "${prev_line}${clear}${ifaces//|/ }"

### Detect Subnets
printf "${bold}%${spacing}s${clear}: ...\n${red}" "Subnets"
# print all ipv4 subnets, filter for just the ones associated with our physical interfaces,
# grab the unique ones and join them by commas
#
# sed -n followed by "s///p" means find and print (with replacements) only the lines containing a match
# '::-1' strips the last character (trailing |)
# 'eno1|eno2|' becomes "s/ dev (eno1|eno2).+//p"
# (eno1|eno2) is a matched group of possible values (| means OR)
# .+ is a catch all to prevent printing the rest of the line
#
# Example Input:
#   10.0.0.0/24 dev eno1 proto kernel src 10.0.0.212 metric 600
#   192.168.1.0/24 dev eno2 proto kernel src 192.168.1.134 metric 900
#   172.17.0.0/16 dev docker0 proto kernel src 172.17.0.1 linkdown
#
# Example Output:
#   10.0.0.0/24
#   192.168.1.0/24
#
# Explanation:
# - The first line matched the 'eno1' interface, so everything starting from " dev eno1 ..."
#     is stripped out, and we are left with just the subnet (10.0.0.0/24).
# - The second line matched the 'eno2' interface, same process as before and we are left with just the subnet.
# - The third line does not match either interface and is not printed.

subnets=$(
    # Print all IPv4 routes, one per line
    ip -4 -o route list scope link |
        # Regex match it against all of our online physical interfaces
        sed -En "s/ dev (${ifaces::-1}).+//p" |
        # Remove [link-local subnet](https://en.wikipedia.org/wiki/Link-local_address) using grep reverse match (-v)
        grep -v "169.254.0.0/16" |
        # Sort and remove potential duplicates
        sort -u |
        # Merge all lines into a single line separated by commas (no trailing ,)
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
