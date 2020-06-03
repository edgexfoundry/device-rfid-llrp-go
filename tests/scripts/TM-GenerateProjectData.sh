#! /bin/bash
# Copyright (C) 2020 Intel Corporation
# SPDX-License-Identifier: Apache-2.0

vREPORT_TEXT_TEMPLATE="${TM_TRIGGER_PATH}/${TM_REPORT_TEML}"
vPLT="platform.yaml"

if [[ ${vREPORT_TEXT_TEMPLATE} == "" ]]; then
	echo "The Report Template name is empty !"
	exit 1
fi

# Pointer to start the project specific section
sed -i '/KGF/, $d' ${vREPORT_TEXT_TEMPLATE}

## Project specific data reporting starts here
echo -e "\t\tPLT-KVM_HOST:   `cat ${vPLT} | shyaml get-value kvmhost.host_address` <br>
\t\tPLT-KVM_PORT:	`cat ${vPLT} | shyaml get-value kvmhost.ssh_port` <br>
\t\tPLT-VM_Domain_Name:   `cat ${vPLT} | shyaml get-value virtual_machines.retail_node_installer.domain_name` <br>
\t\tPLT-VM_HOST:   `cat ${vPLT} | shyaml get-value virtual_machines.retail_node_installer.host_address` <br>
\t\tPLT-HTTP_SERVER:   `cat ${vPLT} | shyaml get-value http_server.host_address` <br>
\t\tPLT-HTTP_PORT:   `cat ${vPLT} | shyaml get-value http_server.http_port` <br>" >> ${vREPORT_TEXT_TEMPLATE}
