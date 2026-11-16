package artifacts

// CDNs 是 artifact 页面除自身文件之外唯一可以加载资源的公共 CDN（非严格模式的沙箱策略放行它们；给 agent 的教程列出它们）。
var CDNs = []string{
	"https://cdnjs.cloudflare.com",
	"https://cdn.jsdelivr.net",
	"https://unpkg.com",
	"https://esm.sh",
	"https://cdn.tailwindcss.com",
	"https://fonts.googleapis.com",
	"https://fonts.gstatic.com",
}
