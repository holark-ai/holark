import styles from './LaunchError.module.css'

export function LaunchError({ reason }: { reason: 'invalid-token' | 'connection' }) {
  return (
    <main className={styles.page}>
      <section className={styles.panel} role="alert" aria-labelledby="launch-error-title">
        <h1 id="launch-error-title">Could not open Holark</h1>
        <p>{reason === 'invalid-token'
          ? 'This launch link is invalid or has expired. Open the latest link printed by Holark in your terminal.'
          : 'Could not connect to Holark. Check that it is running, then reload this page.'}</p>
      </section>
    </main>
  )
}
