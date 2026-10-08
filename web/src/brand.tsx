/** Small line-art mark shared by the workspace and login page. */
export function CatMark() {
  return (
    <svg
      viewBox="0 0 32 32"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.65"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M7 13 6 4l8 5a15 15 0 0 1 4 0l8-5-1 9c2 2 3 4 3 7 0 6-5 9-12 9S4 26 4 20c0-3 1-5 3-7Z" />
      <path d="M11 17v2m10-2v2m-7 3 2 2 2-2m-2 2v2M3 20l5 1m-5 3 5-1m21-3-5 1m5 3-5-1" />
    </svg>
  );
}

export function Brand() {
  return (
    <div className="brand" aria-label="catbot">
      <span className="brand-mark">
        <CatMark />
      </span>
      <div>
        catbot<small>YOUR AI COMPANION</small>
      </div>
    </div>
  );
}
