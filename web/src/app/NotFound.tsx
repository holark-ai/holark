import { ArrowLeft, FileQuestion } from 'lucide-react'
import { Link } from 'react-router-dom'
import styles from './NotFound.module.css'

export function NotFound() {
  return (
    <main className={styles.page}>
      <div className={styles.card}>
        <span className={styles.icon}><FileQuestion /></span>
        <span className={styles.code}>404</span>
        <h1>That page isn't in this workspace</h1>
        <p>The address may be incorrect, or the repository does not include this path.</p>
        <Link to="/"><ArrowLeft /> Return to Holark</Link>
      </div>
    </main>
  )
}
