#
#
# Copyright (C) 2020 Intel Corporation
#
# SPDX-License-Identifier: Apache-2.0
#

# This example uses the standalone cmd/llrp executable
# to connect to an Impinj Reader, send it Custom messages,
# singulating for 5s, and reading the TID of each tag.
# It shows how the LLRP library can be used without EdgeX,
# but doing so requires launching new instances per Reader,
# implementing your own discovery and reconnection logic (if applicable),
# and collecting the output data to forward/process elsewhere.

READER_ADDR='192.168.86.88:5084'
PROJ_DIR=$(dirname "$0")'/..' # assumes script is in {project root}/examples
EXAMPLES="${PROJ_DIR}/examples"

# build the executable
MOD_NAME=$(go list "${PROJ_DIR}")
CMD_DIR="${PROJ_DIR}/cmd/llrp"
go build -o "${CMD_DIR}/llrp" "${MOD_NAME}/cmd/llrp"

# The llrp executable has more options; run it with the -help flag to see them.
"./${CMD_DIR}/llrp" -rfid=${READER_ADDR} \
  -custom="${EXAMPLES}/ImpinjEnableCustomExt.json" \
  -access="${EXAMPLES}/AccessSpec.json" \
  -ro="${EXAMPLES}/ImpinjReadFast.json" \
  -watch-for=5s
