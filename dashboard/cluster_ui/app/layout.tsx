import './globals.css'
import type { Metadata } from 'next'

export const metadata: Metadata = {
  title: 'Aegis — Distributed Commit Log & Event Stream Engine',
  description: 'First-Principles Distributed Commit Log on Windows 11 with Zero-Copy Win32 Memory-Mapped Storage and Raft Consensus',
}

export default function RootLayout({
  children,
}: {
  children: React.ReactNode
}) {
  return (
    <html lang="en" className="dark">
      <body className="min-h-screen bg-[#070709] text-white antialiased selection:bg-rose-500 selection:text-white">
        {children}
      </body>
    </html>
  )
}
