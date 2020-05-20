#!/bin/bash

source "textutils.sh"

clear
printDatedMsg "Checking Internet connectivity"
PING1="$(ping -c 1 8.8.8.8)"

if [[ $PING1 == *"unreachable"* ]]; then
    printDatedErrMsg "ERROR: No network connection found, exiting."
    exit 1
elif [[ $PING1 == *"100% packet loss"* ]]; then
    printDatedErrMsg "ERROR: No Internet connection found, exiting."
    exit 1
else
    printDatedOkMsg "Connectivity OK"
fi

echo
printDatedMsg "Updating apt..."
sudo apt update >>/dev/null 2>&1

printDatedMsg "Checking for docker..."
command -v docker
if [ $? -ne 0 ]; then
    printDatedInfoMsg "INFO: docker not found, installing..."
    sudo apt -y install docker.io
    if [ $? -ne 0 ]; then
        printDatedErrMsg "ERROR: Problem installing docker"
        exit 1
    else
        printDatedOkMsg "OK: Installed docker"
        docker --version
    fi
else
    printDatedOkMsg "OK: found docker"
    docker --version
fi

printDatedMsg "Installing the following dependencies..."
printDatedMsg "    curl ntpdate git"
echo
sudo apt -y install curl  git
if [ $? -ne 0 ]; then
    printDatedErrMsg "ERROR: There was a problem installing dependencies, exiting"
    exit 1
fi

printDatedMsg "Checking for docker-compose..."
command -v docker-compose
if [ $? -ne 0 ]; then
    printDatedInfoMsg "INFO: docker-compose not found, installing..."
    sudo curl -L "https://github.com/docker/compose/releases/download/1.25.4/docker-compose-$(uname -s)-$(uname -m)" -o /usr/local/bin/docker-compose && sudo chmod a+x /usr/local/bin/docker-compose
    if [ $? -ne 0 ]; then
        printDatedErrMsg "ERROR: Problem installing docker-compose"
        exit 1
    else
        printDatedOkMsg "OK: Installed docker-compose"
        docker-compose --version
    fi
else
    printDatedOkMsg "OK: found docker-compose"
    docker-compose --version
fi

