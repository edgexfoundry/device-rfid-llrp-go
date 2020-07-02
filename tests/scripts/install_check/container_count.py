# Copyright (C) 2020 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

import json
import random
import requests
import time
import sys
import pdb 
import subprocess

#Docker containers list for device LLRP service
myDockerList = [
"edgex-kuiper",
"edgex-sys-mgmt-agent",
"edgex-ui-go",
"edgex-device-llrp",
"edgex-app-service-configurable-rules",
"edgex-core-data",
"edgex-core-command",
"edgex-core-metadata",
"edgex-support-notifications",
"edgex-support-scheduler",
"edgex-core-consul",
"edgex-redis"
]

class container_count():
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)

    def verify_containers_running(self, testType):
        try:
            theList = subprocess.check_output(["docker", "ps", "--format", "\"{{.Names}}\""])
            theList = theList.decode("utf-8") 
            # theList = subprocess.run(["docker", "ps", "--format", "\"{{.Names}}\""], capture_output=True)
            print("The List: ", theList, file=sys.stderr )
        except Exception as e:
            print("Check_output failed.", file=sys.stderr)
            print(str(e), file=sys.stderr )
            print("------", file=sys.stderr )
            return False

        print(len(myDockerList),file=sys.stderr)
        print(len(theList),file=sys.stderr)
        for i in range(len(myDockerList)):
            # TypeError: a bytes-like object is required, not 'str'
            if myDockerList[i] not in theList:
                print("Expected container list:\n", myDockerList, file=sys.stderr )
                print("\n\nActual container list:\n", theList, file=sys.stderr )
                # print("Expected container list:\n", myDockerList[i] )
                # print("\n\nActual container list:\n", theList )
                # print(">",theList[i],file=sys.stderr )
                print("]",myDockerList[i],file=sys.stderr )
                return False

        return True
