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
      <head>
        <link rel="preconnect" href="https://fonts.googleapis.com" />
        <link rel="preconnect" href="https://fonts.gstatic.com" crossOrigin="anonymous" />
        <link
          href="https://fonts.googleapis.com/css2?family=Instrument+Serif:ital@1&family=Inter+Tight:wght@400;500;600;700;800&family=Inter:wght@400;500;600&display=swap"
          rel="stylesheet"
        />
      </head>
      <body className="min-h-screen bg-[#120400] text-white antialiased selection:bg-rose-500 selection:text-white">
        {children}
      </body>
    </html>
  )
}
