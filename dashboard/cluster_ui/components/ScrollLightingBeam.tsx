'use client'

import React from 'react'
import { motion, useScroll, useSpring, useTransform } from 'framer-motion'

export default function ScrollLightingBeam() {
  const { scrollYProgress } = useScroll()
  const smoothProgress = useSpring(scrollYProgress, {
    stiffness: 80,
    damping: 25,
    restDelta: 0.001,
  })

  // Transform scroll into spotlight Y offset and dynamic beam heights
  const beamY = useTransform(smoothProgress, [0, 1], ['0%', '100%'])
  const glowOpacity = useTransform(smoothProgress, [0, 0.5, 1], [0.8, 1, 0.8])

  return (
    <div className="pointer-events-none fixed inset-0 z-0 overflow-hidden">
      {/* Ambient Top Spotlight Light Beam (Warm Crimson / Orange) */}
      <div className="absolute top-0 left-1/2 -translate-x-1/2 w-[800px] h-[500px] bg-gradient-to-b from-rose-500/15 via-orange-500/5 to-transparent blur-3xl opacity-80" />

      {/* Dynamic Scroll-Tracking Spotlight Orb */}
      <motion.div
        style={{
          top: beamY,
          opacity: glowOpacity,
        }}
        className="absolute left-1/2 -translate-x-1/2 -translate-y-1/2 w-[600px] h-[600px] rounded-full bg-gradient-to-r from-rose-600/10 via-orange-500/10 to-amber-500/5 blur-[120px]"
      />

      {/* Subtle Matrix Grid Overlay */}
      <div className="absolute inset-0 bg-[linear-gradient(to_right,#ffffff05_1px,transparent_1px),linear-gradient(to_bottom,#ffffff05_1px,transparent_1px)] bg-[size:4rem_4rem] [mask-image:radial-gradient(ellipse_60%_50%_at_50%_0%,#000_70%,transparent_100%)]" />
    </div>
  )
}
