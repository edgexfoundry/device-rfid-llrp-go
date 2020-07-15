#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

HOST=localhost
DATA_PORT=48080
CURL_OPTS="-o-"
REMOVE_NULLS=0

usage() {
    echo "usage:"
    echo "  $0 [OPTS] TARGET"
    echo ""
    echo "TARGETS:     reports tags epcs notifications"
    echo ""
    echo "OPTS:"
    echo "    -h | --host HOST    edgex-core-data host   default: localhost"
    echo "    -p | --port PORT    edgex-core-data port   default: 48080"
    echo "    -v | --verbose      verbose curl output"
    echo "    -s | --silent       silent curl output"
    echo "    -f | --filter-null  remove null values"
}

if [[ $# -lt 1 ]]; then
  usage; exit
fi

while [[ "$1" =~ ^- && ! "$1" == "--" ]]; do case $1 in
  -h | --host )
    shift; HOST=$1
    ;;
  -p | --core-cmds-port )
    shift; DATA_PORT=$1
    ;;
  -v | --verbose )
    CURL_OPTS="-vvvvo-"
    ;;
  -s | --silent )
    CURL_OPTS='-so-'
    ;;
  -f | --filter-null)
    REMOVE_NULLS=1
    ;;
  *)
    echo "unknown flag $1"
    usage; exit
    ;;
esac; shift; done
if [[ "$1" == '--' ]]; then shift; fi

get() {
  TARGET=$1
  curl ${CURL_OPTS} ${HOST}:${DATA_PORT}/api/v1/reading/name/"${TARGET}"/1000 | \
    jq '.[].value|fromjson'
}

if [[ $# -lt 1 ]]; then
    echo "missing target"
    usage; exit
fi

case "$1" in
    reports)
      if [[ $REMOVE_NULLS -eq 1 ]]; then
        shift; get ROAccessReport | jq 'with_entries(select(.value!=null))|select(any(.))|.[]|.[]|with_entries(select(.value!=null))'
      else
        shift; get ROAccessReport
      fi
        ;;
    notifications)
        shift; get ReaderEventNotification
        ;;
    tags)
      if [[ $REMOVE_NULLS -eq 1 ]]; then
        shift; get ROAccessReport | \
          jq '.TagReportData[]?|with_entries(select(.value!=null))|.+{EPC:(.EPCData.EPC//.EPC96.EPC)}|del(.EPCData)|del(.EPC96)'
      else
        shift; get ROAccessReport | \
          jq '.TagReportData[]?|.+{EPC:(.EPCData.EPC//.EPC96.EPC)}|del(.EPCData)|del(.EPC96)'
      fi
    ;;
    epcs)
        shift; get ROAccessReport | \
          jq '.TagReportData[]?|.EPC96.EPC//.EPCData.EPC' | \
          tr -d '"' | base64 -d | od --endian=big -t x2 -An -w12 -v | sort | uniq -c
    ;;
    *)
        echo "unknown target: $1"
        usage
        ;;
esac


