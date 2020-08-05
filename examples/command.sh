#!/usr/bin/env bash
#
#
# Copyright (C) 2020 Intel Corporation
#
# SPDX-License-Identifier: Apache-2.0
#

# command.sh - simple script to interact with EdgeX's core-command
#   to see and manipulate ROs, reader config, and reader capabilities.
#   Use --help to view usage.

set -euo pipefail
IFS=$'\n\t'

HOST=localhost
CMDS_PORT=48082
CURL_OPTS="-o-"
DEVICE=""

usage() {
    echo "usage:"
    echo "  $0 [OPTS] list LISTABLE"
    echo "  $0 [OPTS] get TARGET"
    echo "  $0 [OPTS] SET_ACTION TARGET FILEPATH"
    echo ""
    echo "LISTABLE:    devices commands"
    echo "SET_ACTIONS: set add enable start stop disable delete"
    echo "TARGETS:     ReaderConfig ReaderCapabilities ROSpec AccessSpec"
    echo ""
    echo "OPTS:"
    echo "    -d | --device NAME  specific device to use     default: all LLRP devices"
    echo "    -h | --host HOST    edgex-core-commands host   default: localhost"
    echo "    -p | --port PORT    edgex-core-commands port   default: 48082"
    echo "    -v | --verbose      use verbose curl output"
    echo "    -s | --silent       use silent curl output"
    echo ""
    echo "examples:"
    echo "      $0 list devices"
    echo "      $0 get ReaderCapabilities"
    echo "      $0 set ReaderConfig path/to/config.json"
    echo "      $0 set ReaderConfig <(jq -n '{KeepAliveSpec: {Trigger: 1, Interval: 1000}}')"
    echo "      $0 add ROSpec path/to/ROSpec.json"
    echo "      $0 enable ROSpec 1"
    echo "      $0 delete ROSpec 0"
}

if [[ $# -lt 1 ]]; then
    usage; exit
fi

while [[ "$1" =~ ^- && ! "$1" == "--" ]]; do case $1 in
  -h | --host )
    shift; HOST=$1
    ;;
  -d | --device )
    shift; DEVICE=$1
    ;;
  -p | --core-cmds-port )
    shift; CMDS_PORT=$1
    ;;
  -v | --verbose )
    CURL_OPTS="-vvvvo-"
    ;;
  -s | --silent )
    CURL_OPTS='-so-'
    ;;
  --help )
    usage; exit
    ;;
  *)
    echo "unknown flag $1"
    usage; exit
    ;;
esac; shift; done
if [[ "$1" == '--' ]]; then shift; fi

devices() {
    if [[ -n "$DEVICE" ]]; then
        echo "$DEVICE"
    else
        curl ${CURL_OPTS} "$HOST:$CMDS_PORT/api/v1/device" | jq '[.[]|select(.labels[]=="LLRP")]'
    fi
}

put_file() {
    if [[ $# -ne 3 ]]; then
        usage; exit
    fi
    
    CMD_NAME="${1^}${2}"
    TYPE=${2}
    FILE_PATH="${3}"
    
    devices | jq '.[].commands[]|select(.name=="'"$CMD_NAME"'")|.put.url' | \
            sed -e "s/edgex-core-command/${HOST}/" | \
            xargs -L1 curl ${CURL_OPTS} -X PUT -H 'Content-Type: application/json' \
            --data '@'<(jq '.|{'"$TYPE"': @text}' "$FILE_PATH")
}

put_num() {
    if [[ $# -ne 3 ]]; then
        usage; exit
    fi
    
    CMD_NAME="${1^}${2}"
    TYPE=${2}
    NUM="${3}"
    
    devices | jq '.[].commands[]|select(.name=="'"$CMD_NAME"'")|.put.url' | \
            sed -e "s/edgex-core-command/${HOST}/" | \
            xargs -L1 curl ${CURL_OPTS} -X PUT -H 'Content-Type: application/json' \
            --data '{"'"$TYPE"'": "'"$NUM"'"}'
}

get() {
    if [[ $# -ne 1 ]]; then
        usage; exit
    fi
    
    CMD_NAME="Get${1}"
    
    devices | jq '.[].commands[]|select(.name=="'"$CMD_NAME"'")|.get.url' | \
            sed -e "s/edgex-core-command/${HOST}/" | \
            xargs -L1 curl ${CURL_OPTS} | jq '.readings[].value|fromjson'
}

list() {
    if [[ $# -ne 1 ]]; then
        echo "list takes one argument"
        usage; exit
    fi
    
    case "$1" in 
    devices)
        devices | jq '.[].name' | tr -d '"'
        ;;
    commands)
        devices | jq '.[].commands[]|{name, url:(.get.url // .put.url), get:(.get.path!=null),put:(.put.path!=null)}' | \
             jq -s '.|=.'
        ;;
    *)
        echo "unknown list request: $1"
        usage; exit
        ;;
    esac
}

if [[ $# -lt 1 ]]; then
    echo "missing command"
    usage; exit
fi

case "$1" in
    list)
        shift; list "$@"
        ;;
    get)
        shift; get "$@"
        ;;
    set | add)
        put_file "$@"
        ;;
    enable | start | stop | disable | delete)
        put_num "$@"
        ;;
    *)
        echo "unknown ACTION: $1"
        usage
        ;;
esac


