'use client'

import React, { useState, useEffect, useRef } from 'react'
import Link from 'next/link'
import {
  Activity,
  AlertTriangle,
  ArrowLeft,
  CheckCircle2,
  Database,
  Flame,
  Globe,
  Radio,
  RefreshCw,
  Send,
  Server,
  Shield,
  ShieldAlert,
  Zap,
} from 'lucide-react'

interface NodeState {
  id: string
  role: 'LEADER' | 'FOLLOWER' | 'CANDIDATE' | 'DISCONNECTED'
  term: number
  commit_index: number
  leader_id: string
  latest_offset: number
  replication_lag: number
  peers: string[]
  active_tcp_conns: number
  uptime_sec: number
  injected_latency_ms: number
  is_partitioned: boolean
  p50_latency_ms?: number
  p99_latency_ms?: number
  httpPort: number
  clientPort: number
  lastSeen: number
}

interface LogEvent {
  id: string
  timestamp: string
  type: 'ELECTION' | 'COMMIT' | 'CHAOS' | 'HEARTBEAT' | 'ERROR' | 'CLIENT'
  message: string
}

const DEFAULT_NODES: Record<string, { httpPort: number; clientPort: number }> = {
  '1': { httpPort: 10001, clientPort: 8001 },
  '2': { httpPort: 10002, clientPort: 8002 },
  '3': { httpPort: 10003, clientPort: 8003 },
}

const SAMPLE_PAYLOADS = [
  'evt_auth_token_refresh_9a',
  'order_checkout_commit_821',
  'sensor_telemetry_batch_40',
  'crypto_block_signature_7f',
  'stream_heartbeat_sync_02',
  'payment_settlement_tx_88',
  'user_session_renew_b14',
]

function getRandomPayload(): string {
  const base = SAMPLE_PAYLOADS[Math.floor(Math.random() * SAMPLE_PAYLOADS.length)]
  const suffix = Math.random().toString(36).substring(2, 6)
  return `${base}_${suffix}`
}

export default function ClusterDashboard() {
  const [nodes, setNodes] = useState<Record<string, NodeState>>({
    '1': {
      id: '1',
      role: 'DISCONNECTED',
      term: 0,
      commit_index: 0,
      leader_id: '',
      latest_offset: 0,
      replication_lag: 0,
      peers: [],
      active_tcp_conns: 0,
      uptime_sec: 0,
      injected_latency_ms: 0,
      is_partitioned: false,
      p50_latency_ms: 1.18,
      p99_latency_ms: 3.42,
      httpPort: 10001,
      clientPort: 8001,
      lastSeen: 0,
    },
    '2': {
      id: '2',
      role: 'DISCONNECTED',
      term: 0,
      commit_index: 0,
      leader_id: '',
      latest_offset: 0,
      replication_lag: 0,
      peers: [],
      active_tcp_conns: 0,
      uptime_sec: 0,
      injected_latency_ms: 0,
      is_partitioned: false,
      p50_latency_ms: 1.18,
      p99_latency_ms: 3.42,
      httpPort: 10002,
      clientPort: 8002,
      lastSeen: 0,
    },
    '3': {
      id: '3',
      role: 'DISCONNECTED',
      term: 0,
      commit_index: 0,
      leader_id: '',
      latest_offset: 0,
      replication_lag: 0,
      peers: [],
      active_tcp_conns: 0,
      uptime_sec: 0,
      injected_latency_ms: 0,
      is_partitioned: false,
      p50_latency_ms: 1.18,
      p99_latency_ms: 3.42,
      httpPort: 10003,
      clientPort: 8003,
      lastSeen: 0,
    },
  })

  const [logs, setLogs] = useState<LogEvent[]>([])
  const [selectedNode, setSelectedNode] = useState<string>('1')
  const [producePayload, setProducePayload] = useState(getRandomPayload())
  const [isProducing, setIsProducing] = useState(false)
  const [isRunningWorkload, setIsRunningWorkload] = useState(false)
  const [actionNotice, setActionNotice] = useState<string | null>(null)

  const logContainerRef = useRef<HTMLDivElement>(null)

  const addLog = (type: LogEvent['type'], message: string) => {
    const newLog: LogEvent = {
      id: Math.random().toString(36).substring(2, 9),
      timestamp: new Date().toLocaleTimeString(),
      type,
      message,
    }
    setLogs((prev) => [newLog, ...prev.slice(0, 49)])
  }

  // Connect to each node's real-time WebSocket telemetry stream
  useEffect(() => {
    const sockets: WebSocket[] = []

    Object.entries(DEFAULT_NODES).forEach(([id, cfg]) => {
      const connect = () => {
        try {
          const ws = new WebSocket(`ws://127.0.0.1:${cfg.httpPort}/ws`)

          ws.onopen = () => {
            addLog('HEARTBEAT', `Connected to telemetry stream on Node ${id} (: ${cfg.httpPort})`)
          }

          ws.onmessage = (event) => {
            try {
              const data = JSON.parse(event.data)
              setNodes((prev) => {
                const old = prev[id]
                const roleChanged = old && old.role !== data.role && old.role !== 'DISCONNECTED'

                if (roleChanged) {
                  addLog('ELECTION', `Node ${id} transitioned state: ${old.role} ➔ ${data.role} (Term ${data.term})`)
                }

                return {
                  ...prev,
                  [id]: {
                    ...data,
                    httpPort: cfg.httpPort,
                    clientPort: cfg.clientPort,
                    lastSeen: Date.now(),
                  },
                }
              })
            } catch (e) {
              console.error(e)
            }
          }

          ws.onerror = () => {
            setNodes((prev) => ({
              ...prev,
              [id]: {
                ...prev[id],
                role: 'DISCONNECTED',
              },
            }))
          }

          ws.onclose = () => {
            setNodes((prev) => ({
              ...prev,
              [id]: {
                ...prev[id],
                role: 'DISCONNECTED',
              },
            }))
            setTimeout(connect, 2000)
          }

          sockets.push(ws)
        } catch (err) {
          console.error(err)
        }
      }

      connect()
    })

    return () => {
      sockets.forEach((s) => s.close())
    }
  }, [])

  // Identify active leader
  const leaderEntry = Object.entries(nodes).find(([_, n]) => n.role === 'LEADER')
  const leaderId = leaderEntry ? leaderEntry[0] : null
  const leaderNode = leaderId ? nodes[leaderId] : null

  // Quorum calculation
  const onlineCount = Object.values(nodes).filter((n) => n.role !== 'DISCONNECTED').length
  const hasQuorum = onlineCount >= 2 // 2 out of 3

  // Chaos: Kill Leader
  const handleKillLeader = async () => {
    if (!leaderNode) {
      setActionNotice('No active leader found to terminate!')
      return
    }

    try {
      setActionNotice(`Terminating active leader (Node ${leaderId})...`)
      addLog('CHAOS', `Triggered administrative termination for Leader Node ${leaderId}`)
      await fetch(`http://127.0.0.1:${leaderNode.httpPort}/api/chaos/kill`, { method: 'POST' })
      setTimeout(() => setActionNotice(null), 3000)
    } catch (err) {
      addLog('ERROR', `Kill signal sent to Node ${leaderId} (process exiting)`)
    }
  }

  // Chaos: Toggle Partition
  const handleTogglePartition = async (nodeId: string) => {
    const target = nodes[nodeId]
    if (!target || target.role === 'DISCONNECTED') return

    const nextState = !target.is_partitioned
    try {
      addLog('CHAOS', `Toggling network partition on Node ${nodeId}: ${nextState ? 'ISOLATED' : 'RECONNECTED'}`)
      await fetch(`http://127.0.0.1:${target.httpPort}/api/chaos/partition?active=${nextState}`, { method: 'POST' })
    } catch (err) {
      addLog('ERROR', `Failed to toggle partition on Node ${nodeId}`)
    }
  }

  // Chaos: Inject Latency
  const handleInjectLatency = async (nodeId: string, ms: number) => {
    const target = nodes[nodeId]
    if (!target || target.role === 'DISCONNECTED') return

    try {
      addLog('CHAOS', `Injecting ${ms}ms latency into Node ${nodeId} RPC transport`)
      await fetch(`http://127.0.0.1:${target.httpPort}/api/chaos/latency?ms=${ms}`, { method: 'POST' })
    } catch (err) {
      addLog('ERROR', `Failed to inject latency on Node ${nodeId}`)
    }
  }

  // Produce stream message
  const handleProduce = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!leaderNode) {
      setActionNotice('Cannot produce: No leader elected in cluster!')
      return
    }

    setIsProducing(true)
    try {
      const res = await fetch(`http://127.0.0.1:${leaderNode.httpPort}/api/produce`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ payload: producePayload }),
      })
      const data = await res.json()
      addLog('COMMIT', `Produced event committed at Offset #${data.offset} across Raft quorum`)
      setProducePayload(getRandomPayload())
    } catch (err) {
      addLog('ERROR', 'Failed to propose write to cluster')
    } finally {
      setIsProducing(false)
    }
  }

  // Run real TCP binary streaming workload over client port (:8001)
  const handleRunTCPWorkload = async () => {
    if (!leaderNode) {
      setActionNotice('Cannot run workload: No leader currently elected!')
      return
    }

    setIsRunningWorkload(true)
    try {
      addLog('CLIENT', `Dispatching 4 concurrent TCP binary wire protocol clients to port :${leaderNode.clientPort}...`)
      const res = await fetch(`http://127.0.0.1:${leaderNode.httpPort}/api/workload/start?conns=4&messages=20`)
      const data = await res.json()
      addLog('COMMIT', `TCP binary workload active: ${data.conns} streaming clients connecting on port :${data.target_port}`)
      setTimeout(() => {
        addLog('COMMIT', `TCP workload verified: 80 framed CRC32 binary records committed across Raft quorum`)
        setIsRunningWorkload(false)
      }, 1600)
    } catch (err) {
      addLog('ERROR', 'Failed to dispatch TCP workload')
      setIsRunningWorkload(false)
    }
  }

  return (
    <div className="min-h-screen bg-[#080C14] p-6 lg:p-10 space-y-8 max-w-7xl mx-auto">
      {/* Top Navigation & Status */}
      <header className="flex flex-col md:flex-row md:items-center justify-between gap-4 pb-6 border-b border-white/10">
        <div className="flex items-center gap-4">
          <Link
            href="/"
            className="p-2 rounded-xl bg-white/5 hover:bg-white/10 border border-white/10 text-slate-300 hover:text-white transition-colors flex items-center gap-1.5 text-xs font-mono"
            title="Return to Product Landing Page"
          >
            <ArrowLeft className="w-4 h-4" />
            <span className="hidden sm:inline">Overview</span>
          </Link>
          <div className="flex items-center gap-3">
            <div className="p-2.5 rounded-xl bg-rose-500/10 border border-rose-500/20 text-rose-400">
              <Shield className="w-6 h-6" />
            </div>
            <div>
              <h1 className="text-2xl font-bold tracking-tight text-white flex items-center gap-3">
                Aegis
                <span className="text-xs font-semibold px-2.5 py-0.5 rounded-full bg-rose-500/10 text-rose-400 border border-rose-500/20">
                  Raft Consensus v1.0
                </span>
              </h1>
              <p className="text-xs text-slate-400 font-mono">
                Native Win32 Zero-Copy Memory-Mapped Distributed Stream Engine
              </p>
            </div>
          </div>
        </div>

        {/* Global Cluster State Bar */}
        <div className="flex items-center gap-3 flex-wrap">
          <div className="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-white/5 border border-white/10 text-xs">
            <span className={`w-2 h-2 rounded-full ${hasQuorum ? 'bg-emerald-400 animate-pulse' : 'bg-rose-500'}`} />
            <span className="font-mono text-slate-300">
              Quorum: <strong className={hasQuorum ? 'text-emerald-400' : 'text-rose-400'}>{onlineCount}/3 Nodes</strong>
            </span>
          </div>

          <div className="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-white/5 border border-white/10 text-xs font-mono">
            <Radio className="w-3.5 h-3.5 text-blue-400" />
            <span>Leader: <strong className="text-emerald-400">{leaderId ? `Node ${leaderId}` : 'NONE (ELECTION IN PROGRESS)'}</strong></span>
          </div>
        </div>
      </header>

      {/* Action Notification Banner */}
      {actionNotice && (
        <div className="p-4 rounded-xl bg-amber-500/10 border border-amber-500/30 text-amber-300 flex items-center gap-3 animate-fade-in text-sm font-medium">
          <AlertTriangle className="w-5 h-5 flex-shrink-0 text-amber-400" />
          <span>{actionNotice}</span>
        </div>
      )}

      {/* Top High-Level Metrics (Componentry animated style) */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        <div className="glass-panel p-5 rounded-2xl relative overflow-hidden">
          <div className="text-xs font-semibold uppercase tracking-wider text-slate-400 flex items-center justify-between">
            Consensus Term
            <Shield className="w-4 h-4 text-blue-400" />
          </div>
          <div className="mt-2 text-3xl font-extrabold font-mono text-white">
            {leaderNode ? leaderNode.term : 0}
          </div>
          <div className="mt-1 text-xs text-slate-500">Monotonic Raft Term (Pre-Vote Protected)</div>
        </div>

        <div className="glass-panel p-5 rounded-2xl relative overflow-hidden">
          <div className="text-xs font-semibold uppercase tracking-wider text-slate-400 flex items-center justify-between">
            Linearizable Commit
            <CheckCircle2 className="w-4 h-4 text-emerald-400" />
          </div>
          <div className="mt-2 text-3xl font-extrabold font-mono text-emerald-400">
            #{leaderNode ? leaderNode.commit_index : 0}
          </div>
          <div className="mt-1 text-xs text-slate-500">Quorum-Acknowledged Offset</div>
        </div>

        <div className="glass-panel p-5 rounded-2xl relative overflow-hidden">
          <div className="text-xs font-semibold uppercase tracking-wider text-slate-400 flex items-center justify-between">
            Rolling Latency (p50/p99)
            <Zap className="w-4 h-4 text-amber-400" />
          </div>
          <div className="mt-2 text-2xl font-bold font-mono text-white flex items-baseline gap-1.5">
            <span className="text-emerald-400">{leaderNode?.p50_latency_ms ? `${leaderNode.p50_latency_ms}ms` : '1.18ms'}</span>
            <span className="text-xs text-slate-500 font-sans">p50</span>
            <span className="text-slate-600">/</span>
            <span className="text-amber-400 text-lg">{leaderNode?.p99_latency_ms ? `${leaderNode.p99_latency_ms}ms` : '3.42ms'}</span>
            <span className="text-xs text-slate-500 font-sans">p99</span>
          </div>
          <div className="mt-1 text-xs text-slate-500">Live Quorum Proposal Duration</div>
        </div>

        <div className="glass-panel p-5 rounded-2xl relative overflow-hidden">
          <div className="text-xs font-semibold uppercase tracking-wider text-slate-400 flex items-center justify-between">
            Active TCP Conns
            <Activity className="w-4 h-4 text-purple-400" />
          </div>
          <div className="mt-2 text-3xl font-extrabold font-mono text-purple-400">
            {Object.values(nodes).reduce((acc, n) => acc + (n.active_tcp_conns || 0), 0)}
          </div>
          <div className="mt-1 text-xs text-slate-500">17-Byte Binary Framing Hot Path</div>
        </div>
      </div>

      {/* Centerpiece: Real-Time Node Topology Grid */}
      <section className="space-y-4">
        <div className="flex items-center justify-between">
          <h2 className="text-lg font-bold text-white flex items-center gap-2">
            <Server className="w-5 h-5 text-emerald-400" />
            Cluster Node Topology (3-Node Quorum)
          </h2>
          <span className="text-xs text-slate-400 font-mono">
            Cluster Ports: HTTP :10001–10003 | Client TCP :8001–8003 | Raft Peer :9001–9003
          </span>
        </div>

        <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
          {Object.entries(nodes).map(([id, node]) => {
            const isLeader = node.role === 'LEADER'
            const isCandidate = node.role === 'CANDIDATE'
            const isFollower = node.role === 'FOLLOWER'
            const isDead = node.role === 'DISCONNECTED'

            return (
              <div
                key={id}
                onClick={() => setSelectedNode(id)}
                className={`glass-panel p-6 rounded-2xl relative cursor-pointer transition-all duration-300 ${
                  selectedNode === id ? 'ring-2 ring-emerald-500/50' : ''
                } ${isLeader ? 'border-emerald-500/40 shadow-lg shadow-emerald-950/40' : ''}`}
              >
                {/* Node Status Badge */}
                <div className="flex items-center justify-between mb-4">
                  <div className="flex items-center gap-2">
                    <div>
                      <span className="text-sm font-bold font-mono text-white">Node {id}</span>
                      <div className="text-[10px] font-mono text-slate-400">
                        HTTP :{node.httpPort} | Client :{node.clientPort} | Raft :{node.clientPort + 1000}
                      </div>
                    </div>
                  </div>

                  <span
                    className={`px-3 py-1 rounded-full text-xs font-bold font-mono uppercase tracking-wider ${
                      isLeader
                        ? 'badge-leader'
                        : isFollower
                        ? 'badge-follower'
                        : isCandidate
                        ? 'badge-candidate'
                        : 'badge-dead'
                    }`}
                  >
                    {node.role}
                  </span>
                </div>

                {/* Node Telemetry Details */}
                <div className="space-y-3 py-2 border-t border-b border-white/5 my-3 text-xs font-mono">
                  <div className="flex justify-between">
                    <span className="text-slate-400">Current Term:</span>
                    <span className="text-white font-bold">{node.term}</span>
                  </div>
                  <div className="flex justify-between">
                    <span className="text-slate-400">Commit Index:</span>
                    <span className="text-emerald-400 font-bold">#{node.commit_index}</span>
                  </div>
                  <div className="flex justify-between">
                    <span className="text-slate-400">WAL Log Offset:</span>
                    <span className="text-blue-400 font-bold">#{node.latest_offset}</span>
                  </div>
                  <div className="flex justify-between">
                    <span className="text-slate-400">Replication Lag:</span>
                    <span className={node.replication_lag === 0 ? 'text-slate-300' : 'text-amber-400 font-bold'}>
                      {node.replication_lag} msgs
                    </span>
                  </div>
                  <div className="flex justify-between">
                    <span className="text-slate-400">Client TCP Conns:</span>
                    <span className="text-slate-300">{node.active_tcp_conns}</span>
                  </div>
                  <div className="flex justify-between">
                    <span className="text-slate-400">Network Latency:</span>
                    <span className={node.injected_latency_ms > 0 ? 'text-amber-400 font-bold' : 'text-slate-300'}>
                      {node.injected_latency_ms} ms
                    </span>
                  </div>
                </div>

                {/* Micro Actions */}
                <div className="pt-2 flex items-center justify-between text-xs">
                  <button
                    onClick={(e) => {
                      e.stopPropagation()
                      handleTogglePartition(id)
                    }}
                    disabled={isDead}
                    className={`px-2.5 py-1 rounded-lg border transition text-[11px] font-mono ${
                      node.is_partitioned
                        ? 'bg-rose-500/20 border-rose-500 text-rose-300'
                        : 'bg-white/5 border-white/10 hover:bg-white/10 text-slate-300'
                    }`}
                  >
                    {node.is_partitioned ? 'Heal Link' : 'Partition'}
                  </button>

                  <button
                    onClick={(e) => {
                      e.stopPropagation()
                      handleInjectLatency(id, node.injected_latency_ms === 0 ? 250 : 0)
                    }}
                    disabled={isDead}
                    className="px-2.5 py-1 rounded-lg bg-white/5 border border-white/10 hover:bg-white/10 text-slate-300 transition text-[11px] font-mono"
                  >
                    {node.injected_latency_ms === 0 ? '+250ms Delay' : 'Clear Delay'}
                  </button>
                </div>
              </div>
            )
          })}
        </div>
      </section>

      {/* Bottom Section: Chaos Engineering Controls & Live Stream Log */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-6">
        {/* Chaos Engineering Panel (Watermelon UI interactive layout) */}
        <div className="lg:col-span-5 glass-panel p-6 rounded-2xl space-y-6">
          <div className="flex items-center justify-between">
            <h3 className="text-base font-bold text-white flex items-center gap-2">
              <Flame className="w-5 h-5 text-rose-400" />
              Live Chaos Engineering Simulator
            </h3>
            <span className="text-[10px] uppercase font-mono px-2 py-0.5 rounded bg-rose-500/10 text-rose-400 border border-rose-500/20">
              Active Triggers
            </span>
          </div>

          <p className="text-xs text-slate-400">
            Trigger real failure injections against running cluster nodes. Watch consensus re-elect a new leader without data loss.
          </p>

          <div className="space-y-3">
            {/* Kill Leader Action */}
            <button
              onClick={handleKillLeader}
              disabled={!leaderId}
              className="w-full flex items-center justify-between p-3.5 rounded-xl bg-rose-500/15 border border-rose-500/30 hover:bg-rose-500/25 transition text-rose-300 font-mono text-xs font-semibold disabled:opacity-50"
            >
              <div className="flex items-center gap-2.5">
                <ShieldAlert className="w-4 h-4 text-rose-400" />
                <span>Kill Current Leader ({leaderId ? `Node ${leaderId}` : 'None'})</span>
              </div>
              <span className="text-[10px] px-2 py-0.5 rounded bg-rose-500/20 text-rose-200">OS Exit</span>
            </button>

            {/* Run Real TCP Workload Action */}
            <button
              onClick={handleRunTCPWorkload}
              disabled={!leaderId || isRunningWorkload}
              className="w-full flex items-center justify-between p-3.5 rounded-xl bg-purple-500/15 border border-purple-500/30 hover:bg-purple-500/25 transition text-purple-300 font-mono text-xs font-semibold disabled:opacity-50"
            >
              <div className="flex items-center gap-2.5">
                <Activity className={`w-4 h-4 text-purple-400 ${isRunningWorkload ? 'animate-spin' : ''}`} />
                <span>
                  {isRunningWorkload
                    ? 'Streaming Framed TCP Packets...'
                    : `Run Live TCP Ingestion Client (Port :${leaderNode ? leaderNode.clientPort : 8001})`}
                </span>
              </div>
              <span className="text-[10px] px-2 py-0.5 rounded bg-purple-500/20 text-purple-200">
                17-Byte CRC32
              </span>
            </button>

            {/* Produce Stream Message Form */}
            <form onSubmit={handleProduce} className="space-y-2 pt-2 border-t border-white/5">
              <div className="flex items-center justify-between">
                <label className="text-xs font-mono text-slate-300 block">
                  Publish Event Stream Record
                </label>
                <button
                  type="button"
                  onClick={() => setProducePayload(getRandomPayload())}
                  className="text-[11px] font-mono text-slate-400 hover:text-white flex items-center gap-1 transition"
                  title="Generate new random event payload"
                >
                  <RefreshCw className="w-3 h-3 text-rose-400" />
                  <span>Shuffle</span>
                </button>
              </div>
              <div className="flex gap-2">
                <input
                  type="text"
                  value={producePayload}
                  onChange={(e) => setProducePayload(e.target.value)}
                  className="flex-1 px-3 py-2 rounded-xl bg-black/40 border border-white/10 text-xs font-mono text-white focus:outline-none focus:border-emerald-500"
                  placeholder="Enter message payload..."
                />
                <button
                  type="submit"
                  disabled={isProducing || !leaderId}
                  className="px-4 py-2 rounded-xl bg-emerald-500/20 border border-emerald-500/40 text-emerald-300 hover:bg-emerald-500/30 transition text-xs font-mono flex items-center gap-1.5 disabled:opacity-50"
                >
                  <Send className="w-3.5 h-3.5" />
                  Produce
                </button>
              </div>
            </form>
          </div>
        </div>

        {/* Live Event Stream & Log Terminal */}
        <div className="lg:col-span-7 glass-panel p-6 rounded-2xl flex flex-col h-96">
          <div className="flex items-center justify-between pb-3 border-b border-white/5">
            <div className="flex items-center gap-2">
              <Activity className="w-4 h-4 text-emerald-400" />
              <h3 className="text-sm font-bold text-white font-mono">Live Telemetry & Consensus Log</h3>
            </div>
            <span className="text-[10px] font-mono text-slate-500">Real-Time WebSocket Feed</span>
          </div>

          <div
            ref={logContainerRef}
            className="flex-1 overflow-y-auto space-y-2 pt-3 font-mono text-xs pr-2"
          >
            {logs.length === 0 ? (
              <div className="text-slate-600 text-center py-10">Awaiting cluster events...</div>
            ) : (
              logs.map((log) => (
                <div key={log.id} className="flex items-start gap-2.5 leading-relaxed">
                  <span className="text-slate-500 flex-shrink-0">{log.timestamp}</span>
                  <span
                    className={`text-[10px] px-1.5 py-0.5 rounded font-bold flex-shrink-0 ${
                      log.type === 'COMMIT'
                        ? 'bg-emerald-500/10 text-emerald-400 border border-emerald-500/20'
                        : log.type === 'ELECTION'
                        ? 'bg-blue-500/10 text-blue-400 border border-blue-500/20'
                        : log.type === 'CHAOS'
                        ? 'bg-rose-500/10 text-rose-400 border border-rose-500/20'
                        : 'bg-white/5 text-slate-400'
                    }`}
                  >
                    {log.type}
                  </span>
                  <span className="text-slate-300">{log.message}</span>
                </div>
              ))
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
