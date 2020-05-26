#!/bin/bash

# Copyright (C) 2020 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

echo "Remove all EDGEX-FOUNDRY images"
$(docker images -a  | grep "edgexfoundry" | awk '{print $3}' | xargs docker rmi -f)

echo "Cleaning up unused Resources"
$(docker "rmi" "$(docker images -a -q)")

echo "Cleaning all DANGLING images"
$(docker rmi $(docker images -f dangling=true -q))
