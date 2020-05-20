#!/bin/bash

echo "Remove all EDGEX-FOUNDRY imgaes"
$(docker images -a  | grep "edgexfoundry" | awk '{print $3}' | xargs docker rmi -f)

echo "Remove all OTHERS imgaes"
$(docker "rmi" "$(docker images -a -q)")

echo "Remove all DANGLING imgaes"
$(docker rmi $(docker images -f dangling=true -q))
