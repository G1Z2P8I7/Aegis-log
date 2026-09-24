/** @type {import('tailwindcss').Config} */
module.exports = {
  content: [
    './app/**/*.{js,ts,jsx,tsx,mdx}',
    './components/**/*.{js,ts,jsx,tsx,mdx}',
  ],
  theme: {
    extend: {
      fontFamily: {
        display: ['"Inter Tight"', 'system-ui', '-apple-system', 'sans-serif'],
        body: ['Inter', 'system-ui', '-apple-system', 'sans-serif'],
        italic: ['"Instrument Serif"', 'Georgia', 'serif'],
      },
      colors: {
        ground: '#2a0b02',
        'ground-deep': '#120400',
        flame: '#ff3d00',
        'flame-lit': '#ff8a1f',
        background: '#070709',
        card: '#0c0d14',
        'card-border': '#1F2937',
        leader: '#10B981',
        follower: '#3B82F6',
        candidate: '#F59E0B',
        dead: '#EF4444',
      },
      animation: {
        'pulse-glow': 'pulse-glow 2s cubic-bezier(0.4, 0, 0.6, 1) infinite',
        'border-beam': 'border-beam calc(var(--duration)*1s) infinite linear',
      },
      keyframes: {
        'pulse-glow': {
          '0%, 100%': { opacity: 1, filter: 'drop-shadow(0 0 15px rgba(16, 185, 129, 0.6))' },
          '50%': { opacity: 0.6, filter: 'drop-shadow(0 0 5px rgba(16, 185, 129, 0.2))' },
        },
      },
    },
  },
  plugins: [],
}
