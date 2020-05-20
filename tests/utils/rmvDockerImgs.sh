#!/bin/bash

echo "Remove all EDGEX-FOUNDRY images"
$(docker images -a  | grep "edgexfoundry" | awk '{print $3}' | xargs docker rmi -f)

echo "Remove all OTHERS images"
$(docker "rmi" "$(docker images -a -q)")

echo "Remove all DANGLING images"
$(docker rmi $(docker images -f dangling=true -q))
