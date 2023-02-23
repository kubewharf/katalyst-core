# RoadMap - katalyst core functionalities
This roadmap defines the core functionalities and features that katalyst plans to deliver in the future, but the detailed timeline-breakdown will be published in the next quarter.

<br>
<table>
  <tbody>
    <tr>
      <th>Core Functionalities</th>
      <th align="center">Core Features</th>
      <th align="right">Milestone breakdown</th>
    </tr>
    <tr>
      <td>Collocation for Pods with all QoS</td>
      <td>
        <ul>
          <li>Improve the isolation mechanism</li>
            <ul>
              <li>Intel RDT, iocost, tc, cgroup v2 ...</li>
            </ul>
          <li>Improve eviction strategy for more resource dimensions</li>
            <ul>
              <li>PSI based eviction, IO bandwidth based eviction ...</li>
            </ul>
          <li>Improve resource advertising</li>
            <ul>
              <li>numa-aware reclaimed resource ...</li>
            </ul>
          <li>Support all QoS classes</li>
          <li>QoS enhancement</li>
            <ul>
              <li>numa binding, numa exclusive policy ...</li>
            </ul>
        </ul>
      </td>
      <td>
        <ul>
          <li>2023/07/31: v1 release</li>
        </ul>
      </td>
    </tr>
    <tr>
      <td>Dynamic Resource Management</td>
      <td>
        <ul>
          <li>Implement resource estimation algorithm to enhance accuracy</li>
            <ul>
              <li>indicator-based resource estimation ...</li>
              <li>ml-based resource estimation ...</li>
            </ul>
          <li>Design algorithm interface to support more estimation algorithms as plugins</li>
        </ul>
      </td>
      <td>
        <ul>
          <li>2023/07/31: v1 release</li>
        </ul>
      </td>
    </tr>
    <tr>
      <td>Rescheduling Based on Workload Profiling</td>
      <td>
        <ul>
          <li>Provide real-time metrics via custom-metrics-apiserver</li>
          <li>Collect and extract workload profile</li>
          <li>Implement rescheduling to balance the resource usage across the cluster</li>
        </ul>
      </td>
      <td>/</td>
    </tr>
    <tr>
      <td>Topology Aware Resource Scheduling and Allocation</td>
      <td>
        <ul>
          <li>Support all kinds of heterogeneous devices</li>
          <li>Support more topology aware devices(e.g. nic, disk etc)</li>
          <li>Improve device utilization with virtualization mechanism</li>
        </ul>
      </td>
      <td>/</td>
    </tr>
    <tr>
      <td>Elastic Resource Management</td>
      <td>
        <ul>
          <li>Optimize HPA and VPA to support elastic resource management</li>
        </ul>
      </td>
      <td>/</td>
    </tr>
  </tbody>
</table>
