#!/bin/bash

# Copyright 2022 The Katalyst Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


id=$1
cid=$(crictl ps |grep "$id"|awk '{print $1}')
if [ "${cid}x" == "x" ]; then
exit
fi

cp=$(crictl inspect "$cid"|grep cgroupsPath|awk '{print $2}'|tr -d "\""|tr -d ',')
if [ "${cp}x" == "x" ]; then
exit
fi

date
cat /sys/fs/cgroup/cpuset/"$cp"/cpuset.cpus
