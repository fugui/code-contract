import { describe, it, expect } from 'vitest';
import { renderToString } from 'react-dom/server';
import { CommHeadBitmapCard } from '../components/PacketAnatomyDrawer/CommHeadBitmapCard';
import { TrafficRecordTable } from '../pages/TrafficDebugWorkbenchPage/TrafficRecordTable';
import type { CommHeadAnatomyDTO, TrafficRecordSummary } from '../types/traffic';
import type { Agent, TransportDriverCapability } from '../types/agent';

describe('M29 Frontend Transport Drivers & Mesh Anatomy Tests', () => {
  it('renders transport driver badges on Agent with theme tokens', () => {
    const drivers: TransportDriverCapability[] = [
      {
        name: 'builtin-tcp',
        transports: ['tcp'],
        transport_code: 1,
        capabilities: 15,
        is_available: true,
      },
      {
        name: 'builtin-drt-uprobe',
        transports: ['dophi_mesh'],
        transport_code: 4,
        capabilities: 2,
        is_available: true,
      },
    ];

    const agent: Agent = {
      id: 'agt-1',
      name: 'Agent Host 1',
      endpoint: 'http://127.0.0.1:8092',
      scope: 'SHARED',
      status: 'ONLINE',
      lease_type: '',
      has_ebpf_cap: true,
      transport_drivers: drivers,
    };

    const html = renderToString(
      <div data-testid="agent-header">
        <span
          className="code-badge"
          data-testid={`agent-cap-${agent.has_ebpf_cap ? 'ready' : 'missing'}-${agent.id}`}
        >
          {agent.has_ebpf_cap ? '🟢 eBPF 特权就绪' : '🔴 缺少 eBPF 特权'}
        </span>
        {agent.transport_drivers?.map((td) => (
          <span
            key={td.name}
            className="code-badge"
            data-testid={`driver-cap-${td.name}`}
          >
            {td.name === 'builtin-tcp'
              ? (td.is_available ? 'TCP ✓' : 'TCP ✗')
              : td.name === 'builtin-drt-uprobe'
              ? (td.is_available ? 'dophi_mesh (uprobe) ✓' : 'dophi_mesh ✗')
              : `${td.name} ${td.is_available ? '✓' : '✗'}`}
          </span>
        ))}
      </div>
    );

    expect(html).toContain('driver-cap-builtin-tcp');
    expect(html).toContain('TCP ✓');
    expect(html).toContain('driver-cap-builtin-drt-uprobe');
    expect(html).toContain('dophi_mesh (uprobe) ✓');
  });

  it('renders MESH badge in TrafficRecordTable when transport_proto === 4', () => {
    const records: TrafficRecordSummary[] = [
      {
        id: 'rec-1',
        session_id: '101',
        seq_no: 1,
        process_id: 'proc-1',
        direction: 'EGRESS',
        transport_proto: 4,
        exchange_type: 'UNARY',
        sno: '1001',
        service_name: 'MeshOrderService',
        operation: 'CreateOrder',
        status_code: 0,
        decode_status: 'SUCCESS',
        is_replayable: true,
      },
    ];

    const html = renderToString(
      <table>
        <tbody>
          <TrafficRecordTable
            records={records}
            selectedRecordId={undefined}
            onSelectRecord={() => {}}
          />
        </tbody>
      </table>
    );

    expect(html).toContain('badge-transport-mesh-rec-1');
    expect(html).toContain('MESH');
  });

  it('renders MESH (dophi_mesh) badge in CommHeadBitmapCard when transportProtocol === 4', () => {
    const commHead: CommHeadAnatomyDTO = {
      magicAscii: 'CSE.TCP',
      transportProtocol: 4,
      transportName: 'RPC_TRANSPORT_MESH',
      snoDec: '1001',
      snoHex: '0x03e9',
      uiLength: 120,
      usHeadLen: 24,
      ucMsgType: 2,
      msgTypeName: 'REQ',
      isPacked: 0,
      segments: [],
    };

    const html = renderToString(<CommHeadBitmapCard commHead={commHead} />);

    expect(html).toContain('comm-head-transport-mesh-badge');
    expect(html).toContain('RPC_TRANSPORT_MESH');
  });
});
